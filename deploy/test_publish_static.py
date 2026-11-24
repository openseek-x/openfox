import io
import hashlib
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import Mock, patch
from zipfile import ZipFile

import runpy

release = runpy.run_path(str(Path(__file__).with_name("publish-static.py")))
unpack = release["unpack"]

class ArchiveTest(unittest.TestCase):
    def unpack_files(self, folder, items):
        archive = folder / "input.tgz"
        with tarfile.open(archive, "w:gz") as out:
            for name, kind in items:
                info = tarfile.TarInfo(name)
                info.type = kind
                if kind == tarfile.SYMTYPE:
                    info.linkname = "/etc"
                out.addfile(info, io.BytesIO())
        unpack(archive, folder / "stage", "OpenFox-0.1.2-arm64.pkg")

    def test_static_web_and_desktop_layout(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.unpack_files(root, [(p, tarfile.REGTYPE) for p in
                ("web/index.html", "web/static/app.js", "desktop/latest.json", "desktop/OpenFox-0.1.2-arm64.pkg")])
            self.assertTrue((root / "stage/web/static/app.js").is_file())

    def test_rejects_traversal_links_and_unexpected_files(self):
        for name, kind in [("web/static/../../escape", tarfile.REGTYPE),
                           ("web/static/link", tarfile.SYMTYPE),
                           ("desktop/another.pkg", tarfile.REGTYPE)]:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as tmp:
                with self.assertRaisesRegex(ValueError, "unexpected archive"):
                    self.unpack_files(Path(tmp), [(name, kind)])

    def test_download_reassembles_ranges_and_checks_artifact_digest(self):
        payload = bytes(range(251)) * 10000
        buffer = io.BytesIO()
        with ZipFile(buffer, "w") as zipped:
            zipped.writestr("openfox-static.tar.gz", payload)
        body = buffer.getvalue()
        def respond(request, timeout):
            lo, hi = map(int, request.get_header("Range").removeprefix("bytes=").split("-"))
            response = io.BytesIO(body[lo:hi + 1])
            response.status = 206
            response.headers = {"Content-Range": f"bytes {lo}-{hi}/{len(body)}"}
            return response
        download = release["download_artifact"]
        with tempfile.TemporaryDirectory() as tmp, patch.dict(download.__globals__, {
            "build_opener": lambda *args: Mock(open=respond),
        }):
            artifact, bundle = Path(tmp) / "artifact.zip", Path(tmp) / "bundle"
            download("https://productionresultssa15.blob.core.windows.net/path", artifact)
            with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                release["extract_bundle"](artifact, bundle, "0" * 64)
            release["extract_bundle"](artifact, bundle, hashlib.sha256(body).hexdigest())
            self.assertEqual(bundle.read_bytes(), payload)

    def test_rejects_external_artifact_host(self):
        with self.assertRaisesRegex(ValueError, "unexpected Actions artifact host"):
            release["download_artifact"]("https://example.com/foo", Path("unused"))

if __name__ == "__main__":
    unittest.main()
