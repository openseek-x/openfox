import io
from pathlib import Path
import tarfile
import tempfile
import unittest

import runpy

unpack = runpy.run_path(str(Path(__file__).with_name("publish-static.py")))["unpack"]

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

if __name__ == "__main__":
    unittest.main()
