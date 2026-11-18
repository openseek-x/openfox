"""Single-repository EventBridge/SNS -> OpenFox Lambda bridge (stdlib only).

Handler: bridge.lambda_handler. See README.md before deployment. No AWS SDK,
local retries, event logging, redirects, or TLS verification bypasses.
"""

import datetime
import http.client
import json
import math
import os
import re
import ssl
import time
from urllib.parse import urlsplit


MAX_BODY = 256 * 1024
MAX_RESPONSE = 4 * 1024
MAX_RECORDS = 10
IO_TIMEOUT = 10.0
INVOCATION_BUDGET = 28.0
REFERENCE_EVENTS = {"referenceCreated", "referenceUpdated", "referenceDeleted"}
PR_EVENTS = {
    "pullRequestCreated", "pullRequestSourceBranchUpdated",
    "pullRequestStatusChanged", "pullRequestMergeStatusUpdated",
}


class BridgeError(Exception):
    """Only constant, non-sensitive failure codes may be passed to this type."""


def _require(condition, code):
    if not condition:
        raise BridgeError(code)


def _configuration():
    raw = os.environ.get("OPENFOX_BRIDGE_URL", "")
    _require(raw and not any(ord(c) <= 32 or ord(c) == 127 for c in raw),
             "invalid_configuration")
    url = urlsplit(raw)
    _require(url.scheme == "https" and url.hostname and url.username is None
             and url.password is None and not url.query and not url.fragment
             and url.path.startswith("/") and url.port != 0,
             "invalid_configuration")
    token = os.environ.get("OPENFOX_BRIDGE_TOKEN", "")
    _require(re.fullmatch(r"[A-Za-z0-9_-]{43}", token), "invalid_configuration")
    arn = os.environ.get("CODECOMMIT_REPOSITORY_ARN", "")
    match = re.fullmatch(r"arn:aws:codecommit:([a-z0-9-]+):(\d{12}):([A-Za-z0-9_.-]{1,100})", arn)
    _require(match, "invalid_configuration")
    topic = os.environ.get("CODECOMMIT_SNS_TOPIC_ARN", "")
    _require(not topic or re.fullmatch(r"arn:aws:sns:[a-z0-9-]+:\d{12}:[A-Za-z0-9_-]{1,256}", topic),
             "invalid_configuration")
    return url, token, arn, match.group(1), match.group(2), topic


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        _require(key not in result, "invalid_json")
        result[key] = value
    return result


def _invalid_constant(_value):
    raise BridgeError("invalid_json")


def _decode(value):
    try:
        return json.loads(value, object_pairs_hook=_unique_object,
                          parse_constant=_invalid_constant)
    except (ValueError, RecursionError, UnicodeError):
        raise BridgeError("invalid_json") from None


def _bounded_json(value, depth=0):
    _require(depth <= 16, "event_limit")
    if isinstance(value, dict):
        _require(len(value) <= 1000, "event_limit")
        for key, child in value.items():
            _require(isinstance(key, str), "invalid_event")
            _bounded_json(child, depth + 1)
    elif isinstance(value, list):
        _require(len(value) <= 1000, "event_limit")
        for child in value:
            _bounded_json(child, depth + 1)
    else:
        _require(value is None or isinstance(value, (str, bool, int))
                 or isinstance(value, float) and math.isfinite(value), "invalid_event")


def _event_body(event, arn, region, account):
    _require(isinstance(event, dict), "invalid_event")
    _bounded_json(event)
    event_id = event.get("id")
    _require(isinstance(event_id, str) and 0 < len(event_id) <= 128
             and not any(ord(c) < 32 or 127 <= ord(c) <= 159 for c in event_id), "invalid_event")
    _require(event.get("version") == "0" and event.get("source") == "aws.codecommit"
             and event.get("account") == account and event.get("region") == region
             and event.get("resources") == [arn], "event_scope_mismatch")
    timestamp = event.get("time")
    _require(isinstance(timestamp, str) and re.fullmatch(
        r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})", timestamp),
        "invalid_event")
    try:
        datetime.datetime.fromisoformat(timestamp.replace("Z", "+00:00"))
    except ValueError:
        raise BridgeError("invalid_event") from None
    detail = event.get("detail")
    _require(isinstance(detail, dict), "invalid_event")
    kind = detail.get("event")
    _require(isinstance(kind, str), "invalid_event")
    expected = ("CodeCommit Repository State Change" if kind in REFERENCE_EVENTS else
                "CodeCommit Pull Request State Change" if kind in PR_EVENTS else None)
    _require(expected and event.get("detail-type") == expected, "unsupported_event")
    body = json.dumps(event, ensure_ascii=False, sort_keys=True, separators=(",", ":"),
                      allow_nan=False).encode("utf-8")
    _require(len(body) <= MAX_BODY, "event_limit")
    return body


def _bodies(event, arn, region, account, topic):
    _require(isinstance(event, dict), "invalid_event")
    if "Records" not in event:
        return [_event_body(event, arn, region, account)]
    records = event["Records"]
    _require(topic and "source" not in event and isinstance(records, list)
             and 1 <= len(records) <= MAX_RECORDS, "invalid_sns_envelope")
    bodies = []
    for record in records:
        _require(isinstance(record, dict) and record.get("EventSource") == "aws:sns"
                 and record.get("EventVersion") == "1.0", "invalid_sns_envelope")
        sns = record.get("Sns")
        _require(isinstance(sns, dict) and sns.get("Type") == "Notification"
                 and sns.get("TopicArn") == topic, "invalid_sns_envelope")
        _require(isinstance(sns.get("MessageId"), str) and 0 < len(sns["MessageId"]) <= 128
                 and isinstance(sns.get("Message"), str), "invalid_sns_envelope")
        message = sns["Message"].encode("utf-8")
        _require(len(message) <= MAX_BODY, "event_limit")
        bodies.append(_event_body(_decode(message), arn, region, account))
    # All records are validated before any network side effect.
    return bodies


class _Budget:
    def __init__(self, context):
        self.context = context
        self.deadline = time.monotonic() + INVOCATION_BUDGET

    def timeout(self):
        remaining = min(self.deadline - time.monotonic(),
                        self.context.get_remaining_time_in_millis() / 1000.0 - 1.0)
        _require(remaining >= 0.1, "invocation_budget_exhausted")
        return min(IO_TIMEOUT, remaining)


def _post(url, token, body, budget):
    connection = http.client.HTTPSConnection(
        url.hostname, port=url.port, timeout=budget.timeout(), context=ssl.create_default_context())
    response = None
    try:
        connection.connect()
        # Retain the socket while HTTPResponse owns it after Connection: close.
        sock = connection.sock
        sock.settimeout(budget.timeout())
        connection.request("POST", url.path, body=body, headers={
            "Content-Type": "application/json", "Accept": "application/json",
            "Authorization": "Bearer " + token,
        })
        sock.settimeout(budget.timeout())
        response = connection.getresponse()
        # http.client never follows Location. Error bodies may contain secrets.
        _require(response.status == 202, "receiver_rejected")
        _require(response.getheader("Content-Type", "").split(";")[0].strip().lower()
                 == "application/json", "invalid_receiver_response")
        _require(response.getheader("Content-Encoding", "identity").lower() == "identity",
                 "invalid_receiver_response")
        length = response.getheader("Content-Length")
        _require(length is None or length.isdigit() and int(length) <= MAX_RESPONSE,
                 "receiver_response_limit")
        raw = bytearray()
        while True:
            sock.settimeout(budget.timeout())
            chunk = response.read1(MAX_RESPONSE + 1 - len(raw))
            if not chunk:
                break
            raw.extend(chunk)
            _require(len(raw) <= MAX_RESPONSE, "receiver_response_limit")
            if response.isclosed():
                break
        _require(length is None or len(raw) == int(length), "invalid_receiver_response")
        value = _decode(raw.decode("utf-8"))
        _require(isinstance(value, dict) and value.get("accepted") is True
                 and type(value.get("duplicate")) is bool, "invalid_receiver_response")
        return value["duplicate"]
    finally:
        if response is not None:
            response.close()
        connection.close()


def lambda_handler(event, context):
    try:
        budget = _Budget(context)
        url, token, arn, region, account, topic = _configuration()
        bodies = _bodies(event, arn, region, account, topic)
        duplicates = 0
        for body in bodies:
            duplicates += int(_post(url, token, body, budget))
        return {"forwarded": len(bodies), "duplicates": duplicates}
    except BridgeError:
        raise
    except Exception:
        # Never attach the original exception, headers, URL, body, or token.
        raise BridgeError("bridge_failed") from None
