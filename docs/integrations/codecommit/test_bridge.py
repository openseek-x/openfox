"""Controlled tests: no AWS calls, real credentials, or system trust changes."""

import io
import json
import os
import pathlib
import ssl
import subprocess
import tempfile
import threading
import traceback
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import patch

import bridge


ARN = "arn:aws:codecommit:us-east-1:123456789012:Example"
TOPIC = "arn:aws:sns:us-east-1:123456789012:ExampleBridge"
TOKEN = "T" * 43  # Synthetic, never a credential.
ENV = {
    "OPENFOX_BRIDGE_URL": "https://receiver.invalid/api/v1/hooks/git/codecommit/fixture",
    "OPENFOX_BRIDGE_TOKEN": TOKEN,
    "CODECOMMIT_REPOSITORY_ARN": ARN,
    "CODECOMMIT_SNS_TOPIC_ARN": TOPIC,
}


def event(event_id="fixture-event-1"):
    return {
        "version": "0", "id": event_id, "source": "aws.codecommit",
        "account": "123456789012", "region": "us-east-1",
        "time": "2026-11-15T02:03:04Z", "resources": [ARN],
        "detail-type": "CodeCommit Repository State Change",
        "detail": {"event": "referenceCreated", "repositoryName": "Example",
                   "repositoryId": "11111111-1111-4111-8111-111111111111",
                   "referenceType": "branch", "referenceName": "main",
                   "referenceFullName": "refs/heads/main", "commitId": "a" * 40},
    }


def sns(*events):
    return {"Records": [{"EventSource": "aws:sns", "EventVersion": "1.0", "Sns": {
        "Type": "Notification", "TopicArn": TOPIC, "MessageId": "sns-fixture-id",
        "Message": json.dumps(value), "SigningCertURL": "https://unused.invalid/cert",
    }} for value in events]}


class Context:
    def get_remaining_time_in_millis(self):
        return 30000


class FakeSocket:
    def __init__(self):
        self.timeouts = []

    def settimeout(self, timeout):
        self.timeouts.append(timeout)


class FakeResponse:
    def __init__(self, status=202, body=b'{"accepted":true,"duplicate":false}', headers=None):
        self.status = status
        self.raw = io.BytesIO(body)
        self.headers = {"Content-Type": "application/json", **(headers or {})}
        self.closed = False

    def getheader(self, name, default=None):
        return self.headers.get(name, default)

    def read1(self, size):
        return self.raw.read(size)

    def isclosed(self):
        return self.closed

    def close(self):
        self.closed = True


class FakeConnection:
    def __init__(self, response=None, error=None):
        self.response = response or FakeResponse()
        self.error = error
        self.sock = FakeSocket()
        self.closed = False
        self.requests = []

    def connect(self):
        if self.error:
            raise self.error

    def request(self, method, path, body, headers):
        self.requests.append((method, path, body, headers.get("Authorization") == "Bearer " + TOKEN))

    def getresponse(self):
        return self.response

    def close(self):
        self.closed = True


class BridgeTest(unittest.TestCase):
    def setUp(self):
        self.env = patch.dict(os.environ, ENV, clear=True)
        self.env.start()
        self.addCleanup(self.env.stop)

    def invoke(self, value, connections=None, context=None):
        connections = connections or [FakeConnection()]
        with patch.object(bridge.http.client, "HTTPSConnection", side_effect=connections) as factory:
            result = bridge.lambda_handler(value, context or Context())
        return result, factory

    def test_direct_and_sns_replay_keep_id_time_and_identical_canonical_body(self):
        first = event()
        first["detail"]["title"] = "中文 <script> text"
        reordered = dict(reversed(list(first.items())))
        a, b = FakeConnection(), FakeConnection(FakeResponse(body=b'{"accepted":true,"duplicate":true}'))
        result, _ = self.invoke(first, [a])
        duplicate, _ = self.invoke(sns(reordered), [b])
        self.assertEqual(result, {"forwarded": 1, "duplicates": 0})
        self.assertEqual(duplicate, {"forwarded": 1, "duplicates": 1})
        self.assertEqual(a.requests[0][2], b.requests[0][2])
        payload = json.loads(a.requests[0][2])
        self.assertEqual((payload["id"], payload["time"]), (first["id"], first["time"]))
        self.assertTrue(a.requests[0][3])
        self.assertTrue(a.closed and a.response.closed and b.closed)

    def test_all_records_validated_before_first_send(self):
        values = [sns(event(), {**event("second"), "account": "999999999999"}),
                  sns(*[event(str(i)) for i in range(11)]), sns(event())]
        values[-1]["Records"][0]["Sns"]["TopicArn"] += "Wrong"
        for value in values:
            with self.subTest(value_count=len(value["Records"])):
                with patch.object(bridge.http.client, "HTTPSConnection") as factory:
                    with self.assertRaises(bridge.BridgeError):
                        bridge.lambda_handler(value, Context())
                    factory.assert_not_called()

    def test_sns_envelope_and_json_rejections(self):
        malformed = []
        for key, value in [("Type", "SubscriptionConfirmation"), ("Message", "{"),
                           ("Message", '{"id":1,"id":2}'), ("Message", '{"n":NaN}'),
                           ("Message", "x" * (bridge.MAX_BODY + 1)), ("MessageId", "")]:
            wrapped = sns(event())
            wrapped["Records"][0]["Sns"][key] = value
            malformed.append(wrapped)
        wrapped = sns(event())
        wrapped["Records"][0]["EventSource"] = "aws:sqs"
        malformed.extend([wrapped, {"Records": []}, {**sns(event()), "source": "aws.codecommit"}])
        for value in malformed:
            with self.subTest(case=malformed.index(value)):
                with self.assertRaises(bridge.BridgeError):
                    self.invoke(value)
        with patch.dict(os.environ, {"CODECOMMIT_SNS_TOPIC_ARN": ""}):
            with self.assertRaises(bridge.BridgeError):
                self.invoke(sns(event()))

    def test_payload_size_depth_and_collection_limits(self):
        oversized = event()
        oversized["detail"]["title"] = "中" * bridge.MAX_BODY
        deep = event()
        value = deep
        for _ in range(17):
            value["nested"] = {}
            value = value["nested"]
        many = event()
        many["extra"] = [None] * 1001
        for value in [oversized, deep, many, {**event(), "time": "2026-02-30T00:00:00Z"},
                      {**event(), "time": "2026-11-15"}, {**event(), "resources": [ARN, ARN]},
                      {**event(), "id": "x\n"}, {**event(), "extra": float("inf")}]:
            with self.assertRaises(bridge.BridgeError):
                self.invoke(value)

    def test_exact_body_response_and_batch_limits(self):
        value = event()
        value["detail"]["title"] = ""
        size = len(json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode())
        value["detail"]["title"] = "x" * (bridge.MAX_BODY - size)
        connection = FakeConnection(FakeResponse(body=b'{"accepted":true,"duplicate":false}'.ljust(4096)))
        self.assertEqual(self.invoke(value, [connection])[0]["forwarded"], 1)
        self.assertEqual(len(connection.requests[0][2]), bridge.MAX_BODY)
        value["detail"]["title"] += "x"
        with self.assertRaisesRegex(bridge.BridgeError, "event_limit"):
            self.invoke(value)
        self.assertEqual(self.invoke(sns(*[event(str(i)) for i in range(10)]),
                                     [FakeConnection() for _ in range(10)])[0]["forwarded"], 10)

    def test_seven_supported_events_and_pr_shape(self):
        for kind in bridge.REFERENCE_EVENTS | bridge.PR_EVENTS:
            value = event()
            value["detail"]["event"] = kind
            if kind in bridge.PR_EVENTS:
                value["detail-type"] = "CodeCommit Pull Request State Change"
                value["detail"] = {"event": kind, "repositoryNames": ["Example"], "pullRequestId": "1"}
            self.assertEqual(self.invoke(value)[0]["forwarded"], 1)
        value = event()
        value["detail"]["event"] = "approvalRuleCreated"
        with self.assertRaisesRegex(bridge.BridgeError, "unsupported_event"):
            self.invoke(value)

    def test_partial_batch_failure_replay_preserves_already_accepted_body(self):
        a, failed = FakeConnection(), FakeConnection(FakeResponse(status=503))
        with self.assertRaisesRegex(bridge.BridgeError, "receiver_rejected"):
            self.invoke(sns(event(), event("second")), [a, failed])
        repeated = FakeConnection(FakeResponse(body=b'{"accepted":true,"duplicate":true}'))
        result, factory = self.invoke(sns(event(), event("second")), [repeated, FakeConnection()])
        self.assertEqual(result, {"forwarded": 2, "duplicates": 1})
        self.assertEqual(a.requests[0][2], repeated.requests[0][2])
        self.assertEqual(factory.call_count, 2)
        self.assertTrue(failed.closed and failed.response.closed)

    def test_response_status_shape_size_encoding_and_no_redirect(self):
        responses = [FakeResponse(status=status) for status in [200, 301, 302, 307, 308, 400, 401, 409, 429, 503]]
        responses += [FakeResponse(body=b'{"accepted":false,"duplicate":false}'),
                      FakeResponse(body=b'{"accepted":true,"duplicate":1}'),
                      FakeResponse(body=b'{"accepted":true,"accepted":false,"duplicate":false}'),
                      FakeResponse(body=b"x" * 4097),
                      FakeResponse(headers={"Content-Length": "4097"}),
                      FakeResponse(headers={"Content-Length": "100"}),
                      FakeResponse(headers={"Content-Encoding": "gzip"}),
                      FakeResponse(headers={"Content-Type": "text/html"})]
        for response in responses:
            connection = FakeConnection(response)
            with self.subTest(status=response.status):
                with patch.object(bridge.http.client, "HTTPSConnection", return_value=connection) as factory:
                    with self.assertRaises(bridge.BridgeError):
                        bridge.lambda_handler(event(), Context())
                    self.assertEqual(factory.call_count, 1)
                self.assertTrue(connection.closed and response.closed)

    def test_configuration_and_secret_redaction(self):
        for key, value in [("OPENFOX_BRIDGE_URL", "http://receiver.invalid/path"),
                           ("OPENFOX_BRIDGE_URL", "https://user:password@receiver.invalid/path"),
                           ("OPENFOX_BRIDGE_URL", "https://receiver.invalid/path?token=secret"),
                           ("OPENFOX_BRIDGE_URL", "https://receiver.invalid/path#fragment"),
                           ("OPENFOX_BRIDGE_URL", "https://receiver.invalid/\npath"),
                           ("OPENFOX_BRIDGE_TOKEN", "short"),
                           ("CODECOMMIT_REPOSITORY_ARN", "arn:aws-cn:codecommit:region:123456789012:Example")]:
            with patch.dict(os.environ, {key: value}):
                with self.assertRaises(bridge.BridgeError):
                    self.invoke(event())
        connection = FakeConnection(error=OSError(TOKEN))
        try:
            self.invoke(event(), [connection])
        except bridge.BridgeError:
            trace = traceback.format_exc()
            self.assertNotIn(TOKEN, trace)
            self.assertIn("bridge_failed", trace)
        else:
            self.fail("expected sanitized failure")
        self.assertTrue(connection.closed)

    def test_per_io_and_shared_remaining_budget(self):
        connection = FakeConnection()
        _, factory = self.invoke(event(), [connection])
        self.assertEqual(factory.call_args.kwargs["timeout"], 10)
        self.assertTrue(all(0 < value <= 10 for value in connection.sock.timeouts))
        class AlmostExpired(Context):
            def get_remaining_time_in_millis(self):
                return 1050
        with patch.object(bridge.http.client, "HTTPSConnection") as factory:
            with self.assertRaisesRegex(bridge.BridgeError, "invocation_budget_exhausted"):
                bridge.lambda_handler(event(), AlmostExpired())
            factory.assert_not_called()
        with patch.object(bridge.time, "monotonic", side_effect=[100, 127]):
            self.assertEqual(bridge._Budget(Context()).timeout(), 1)
        with patch.object(bridge.time, "monotonic", side_effect=[100, 128]):
            with self.assertRaisesRegex(bridge.BridgeError, "invocation_budget_exhausted"):
                bridge._Budget(Context()).timeout()

    def test_deployment_templates_match_scope_and_optional_write(self):
        directory = pathlib.Path(__file__).parent
        read = json.loads((directory / "iam-read-policy.template.json").read_text())
        optional = json.loads((directory / "iam-pr-backlink-policy.template.json").read_text())
        rule = json.loads((directory / "eventbridge-rule.template.json").read_text())
        self.assertEqual(read["Statement"][0]["Action"], ["codecommit:ListRepositories"])
        self.assertEqual(read["Statement"][0]["Resource"], "*")
        self.assertEqual(optional["Statement"][0]["Action"], ["codecommit:PostCommentForPullRequest"])
        self.assertEqual(optional["Statement"][0]["Resource"], read["Statement"][1]["Resource"])
        self.assertEqual(rule["resources"], read["Statement"][1]["Resource"])
        self.assertEqual(set(rule["detail"]["event"]), bridge.REFERENCE_EVENTS | bridge.PR_EVENTS)
        self.assertEqual(rule["source"], ["aws.codecommit"])


class TLSTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory(prefix="openfox-i13-bridge-tls-")
        cls.path = pathlib.Path(cls.temp.name)
        (cls.path / "cert.cnf").write_text("""[req]
distinguished_name=dn
x509_extensions=ext
prompt=no
[dn]
CN=localhost
[ext]
subjectAltName=DNS:localhost
basicConstraints=critical,CA:TRUE
keyUsage=critical,keyCertSign,digitalSignature,keyEncipherment
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid:always
""")
        subprocess.run(["/usr/bin/openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
                        "-days", "1", "-config", str(cls.path / "cert.cnf"),
                        "-keyout", str(cls.path / "key.pem"), "-out", str(cls.path / "cert.pem")],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        cls.default_context = staticmethod(ssl.create_default_context)

    @classmethod
    def tearDownClass(cls):
        cls.temp.cleanup()

    def setUp(self):
        self.requests = []
        self.response_status = 202
        self.response_body = b'{"accepted":true,"duplicate":false}'
        owner = self
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, _format, *_args):
                pass

            def do_POST(self):
                body = self.rfile.read(int(self.headers["Content-Length"]))
                owner.requests.append((self.path, body, self.headers.get("Authorization") == "Bearer " + TOKEN))
                self.send_response(owner.response_status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(owner.response_body)))
                self.send_header("Connection", "close")
                self.send_header("Location", "https://unused.invalid/never-follow")
                self.end_headers()
                self.wfile.write(owner.response_body)
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(str(self.path / "cert.pem"), str(self.path / "key.pem"))
        self.server.socket = context.wrap_socket(self.server.socket, server_side=True)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.env = patch.dict(os.environ, {**ENV, "OPENFOX_BRIDGE_URL": self.url("localhost")}, clear=True)
        self.env.start()

    def tearDown(self):
        self.env.stop()
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)
        self.assertFalse(self.thread.is_alive())

    def url(self, host):
        return "https://%s:%s/fixture" % (host, self.server.server_port)

    def trusted_context(self):
        return self.default_context(cafile=str(self.path / "cert.pem"))

    def test_default_trust_rejects_unknown_ca_before_authorization(self):
        with self.assertRaisesRegex(bridge.BridgeError, "bridge_failed"):
            bridge.lambda_handler(event(), Context())
        self.assertEqual(len(self.requests), 0)

    def test_trusted_test_ca_accepts_and_closes_real_http_response(self):
        with patch.object(bridge.ssl, "create_default_context", self.trusted_context):
            self.assertEqual(bridge.lambda_handler(event(), Context()), {"forwarded": 1, "duplicates": 0})
        self.assertEqual(len(self.requests), 1)
        self.assertTrue(self.requests[0][2])

    def test_trusted_ca_wrong_hostname_rejected_before_authorization(self):
        with patch.dict(os.environ, {"OPENFOX_BRIDGE_URL": self.url("127.0.0.1")}):
            with patch.object(bridge.ssl, "create_default_context", self.trusted_context):
                with self.assertRaisesRegex(bridge.BridgeError, "bridge_failed"):
                    bridge.lambda_handler(event(), Context())
        self.assertEqual(len(self.requests), 0)

    def test_real_redirect_and_oversize_responses_fail_without_retry(self):
        with patch.object(bridge.ssl, "create_default_context", self.trusted_context):
            self.response_status = 302
            with self.assertRaisesRegex(bridge.BridgeError, "receiver_rejected"):
                bridge.lambda_handler(event(), Context())
            self.assertEqual(len(self.requests), 1)
            self.response_status = 202
            self.response_body = b"x" * 4097
            with self.assertRaisesRegex(bridge.BridgeError, "receiver_response_limit"):
                bridge.lambda_handler(event(), Context())
            self.assertEqual(len(self.requests), 2)


if __name__ == "__main__":
    unittest.main()
