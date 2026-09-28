"""Keep CI's committed web export check aligned with the local build recipe."""

from pathlib import Path
import re
import unittest


ROOT = Path(__file__).resolve().parents[1]


class WebExportCheckTest(unittest.TestCase):
    def test_ci_copies_the_same_export_as_just_web_build(self):
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        justfile = (ROOT / "Justfile").read_text()
        config = (ROOT / "web/next.config.ts").read_text()
        source = re.search(r"cp -a (web/[^ ]+/\.) internal/web/static/app/", justfile)
        self.assertIsNotNone(source, "web-build must copy an export directory")
        self.assertIn(f"cp -a {source.group(1)} internal/web/static/app/", workflow)
        self.assertIn('distDir: ".next-build"', config)


if __name__ == "__main__":
    unittest.main()
