"""Check the issue-template chooser and reporter prompts without network access."""

import pathlib
import unittest


TEMPLATES = pathlib.Path(__file__).resolve().parents[1] / ".github" / "ISSUE_TEMPLATE"


class IssueTemplatesTest(unittest.TestCase):
    def template_body(self, filename, name):
        text = (TEMPLATES / filename).read_text()
        frontmatter, body = text.removeprefix("---\n").split("\n---\n", 1)
        self.assertTrue(text.startswith("---\n"))
        metadata = dict(line.split(": ", 1) for line in frontmatter.splitlines())
        self.assertEqual(metadata["name"], name)
        self.assertTrue(metadata["about"].strip())
        self.assertNotIn("labels", metadata)
        self.assertNotIn("assignees", metadata)
        return body

    def test_bug_report_collects_context_without_guessing(self):
        body = self.template_body("bug_report.md", "Bug report")
        for heading in [
            "Steps to reproduce", "Actual behavior", "Expected behavior",
            "Stage reached", "Video code or Job ID", "Burned-in subtitles",
            "Logs or error line",
        ]:
            with self.subTest(heading=heading):
                self.assertIn("## " + heading, body)
        stage = body.split("## Stage reached\n", 1)[1].split("\n## ", 1)[0]
        for option in ["preparing", "moving", "transcribing", "translating", "delivering", "not sure"]:
            with self.subTest(stage=option):
                self.assertIn(option, stage)
        self.assertNotIn("rendering", stage)
        subtitles = body.split("## Burned-in subtitles\n", 1)[1].split("\n## ", 1)[0]
        for option in ["yes", "no", "not sure"]:
            self.assertIn(option, subtitles)
        self.assertIn("before a Job exists", body)
        self.assertIn("Submission ID if available", body)
        self.assertIn("submission=<Submission ID>", body)
        self.assertIn("Job ID correlates logs across Attempts", body)
        self.assertIn("affected execution", body)

    def test_feature_request_collects_problem_and_desired_behavior(self):
        body = self.template_body("feature_request.md", "Feature request")
        self.assertIn("## Problem to solve", body)
        self.assertIn("## Desired behavior", body)

    def test_chooser_keeps_blank_issues_enabled(self):
        self.assertEqual(
            (TEMPLATES / "config.yml").read_text().strip(),
            "blank_issues_enabled: true",
        )
        self.assertEqual(
            {path.name for path in TEMPLATES.glob("*.md")},
            {"bug_report.md", "feature_request.md"},
        )


if __name__ == "__main__":
    unittest.main()
