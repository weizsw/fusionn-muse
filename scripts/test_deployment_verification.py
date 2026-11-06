import http.server
import json
import os
import re
import shutil
import socketserver
import subprocess
import tempfile
import threading
import unittest

class MockMuseDeploymentHandler(http.server.BaseHTTPRequestHandler):
    scenario = "success"
    submission_polls = 0
    job_polls = 0
    history_job_accessed = False

    def do_POST(self):
        if self.path == "/api/v1/webhook/torrent":
            if MockMuseDeploymentHandler.scenario == "webhook_fail":
                self.send_response(500)
                self.end_headers()
                return
            length = int(self.headers.get("content-length", 0))
            _ = self.rfile.read(length)
            sub_id = "sub-e2e-001"
            self.send_response(202)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({
                "message": "webhook accepted",
                "submission_id": sub_id,
                "status": "pending"
            }).encode("utf-8"))
        else:
            self.send_response(404)
            self.end_headers()

    def do_GET(self):
        if self.path.startswith("/api/v1/tasks/submission/"):
            sub_id = self.path.split("/")[-1]
            MockMuseDeploymentHandler.submission_polls += 1
            if MockMuseDeploymentHandler.scenario in ("success", "job_failed", "artifacts_missing", "light_bypass"):
                if MockMuseDeploymentHandler.submission_polls < 2:
                    resp = {
                        "id": sub_id,
                        "status": "processing",
                        "stage": "preparing",
                        "canonical_task": None
                    }
                else:
                    resp = {
                        "id": sub_id,
                        "status": "processing",
                        "stage": "transcribing",
                        "canonical_task": {"kind": "job", "id": "job-associated-999"}
                    }
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps(resp).encode("utf-8"))
            elif MockMuseDeploymentHandler.scenario == "prep_failed":
                resp = {
                    "id": sub_id,
                    "status": "failed",
                    "stage": "preparing",
                    "error": "sample video too small (<200MB)",
                    "canonical_task": None
                }
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps(resp).encode("utf-8"))
            elif MockMuseDeploymentHandler.scenario == "interrupted":
                resp = {
                    "id": sub_id,
                    "status": "interrupted",
                    "stage": "preparing",
                    "error": "worker shutdown",
                    "canonical_task": None
                }
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps(resp).encode("utf-8"))
            elif MockMuseDeploymentHandler.scenario == "ignored":
                resp = {
                    "id": sub_id,
                    "status": "ignored",
                    "stage": "preparing",
                    "error": "filtered non-media",
                    "canonical_task": None
                }
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps(resp).encode("utf-8"))
            elif MockMuseDeploymentHandler.scenario == "unlinked":
                resp = {
                    "id": sub_id,
                    "status": "processing",
                    "stage": "preparing",
                    "canonical_task": None
                }
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps(resp).encode("utf-8"))
            elif MockMuseDeploymentHandler.scenario == "query_500":
                self.send_response(500)
                self.end_headers()
            elif MockMuseDeploymentHandler.scenario == "query_404":
                self.send_response(404)
                self.end_headers()
        elif self.path.startswith("/api/v1/tasks/job/"):
            job_id = self.path.split("/")[-1]
            MockMuseDeploymentHandler.job_polls += 1
            if job_id == "SNOS-411":
                # Historical job check
                MockMuseDeploymentHandler.history_job_accessed = True
                resp = {
                    "id": "SNOS-411",
                    "status": "completed",
                    "stage": "translating",
                    "is_light": false,
                    "error": "",
                    "job": {"translated_path": "/data/automation/subtitles/SNOS-411.srt"}
                }
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps(resp).encode("utf-8"))
            elif job_id == "job-associated-999":
                if MockMuseDeploymentHandler.scenario == "job_failed":
                    resp = {
                        "id": "job-associated-999",
                        "status": "failed",
                        "stage": "translating",
                        "is_light": False,
                        "error": "LLM API key invalid",
                        "job": {"translated_path": ""}
                    }
                elif MockMuseDeploymentHandler.scenario == "light_bypass":
                    resp = {
                        "id": "job-associated-999",
                        "status": "completed",
                        "stage": "scraping",
                        "is_light": True,
                        "error": "",
                        "job": {
                            "id": "job-associated-999",
                            "is_light": True,
                            "subtitle_detection_reason": "hard_sub",
                            "translated_path": ""
                        }
                    }
                else:
                    if MockMuseDeploymentHandler.job_polls < 2:
                        resp = {
                            "id": "job-associated-999",
                            "status": "processing",
                            "stage": "transcribing",
                            "is_light": False,
                            "error": "",
                            "job": {"translated_path": ""}
                        }
                    else:
                        resp = {
                            "id": "job-associated-999",
                            "status": "completed",
                            "stage": "translating",
                            "is_light": False,
                            "error": "",
                            "job": {
                                "id": "job-associated-999",
                                "is_light": False,
                                "translated_path": "/data/automation/subtitles/SNOS-411.srt"
                            }
                        }
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps(resp).encode("utf-8"))
            else:
                self.send_response(404)
                self.end_headers()
        else:
            self.send_response(404)
            self.end_headers()

    def log_message(self, format, *args):
        pass


def extract_tutorial_bash_script(markdown_path, start_heading, end_heading=None):
    with open(markdown_path, "r", encoding="utf-8") as f:
        content = f.read()
    start_pos = content.find(start_heading)
    if start_pos == -1:
        raise ValueError(f"Heading '{start_heading}' not found in {markdown_path}")
    sub = content[start_pos:]
    if end_heading:
        end_pos = sub.find(end_heading, len(start_heading))
        if end_pos != -1:
            sub = sub[:end_pos]
    blocks = re.findall(r"```bash\s*\n(.*?)\n\s*```", sub, re.DOTALL)
    if not blocks:
        raise ValueError(f"No bash code blocks found under '{start_heading}' in {markdown_path}")
    return "\n\n".join(blocks)


class DeploymentVerificationTests(unittest.TestCase):
    def setUp(self):
        MockMuseDeploymentHandler.scenario = "success"
        MockMuseDeploymentHandler.submission_polls = 0
        MockMuseDeploymentHandler.job_polls = 0
        MockMuseDeploymentHandler.history_job_accessed = False

        self.server = socketserver.TCPServer(("127.0.0.1", 0), MockMuseDeploymentHandler)
        self.port = self.server.server_address[1]
        self.thread = threading.Thread(target=self.server.serve_forever)
        self.thread.daemon = True
        self.thread.start()

        self.tmpdir = tempfile.mkdtemp()
        os.makedirs(f"{self.tmpdir}/torrents/whisparr", exist_ok=True)
        os.makedirs(f"{self.tmpdir}/automation/scraping", exist_ok=True)
        os.makedirs(f"{self.tmpdir}/automation/subtitles", exist_ok=True)

        self.sample_file = f"{self.tmpdir}/torrents/whisparr/SNOS-411.mp4"
        with open(self.sample_file, "wb") as f:
            f.write(b"0" * 1024)

        # Pre-seed scraping video
        self.scraping_video = f"{self.tmpdir}/automation/scraping/SNOS-411.mp4"
        with open(self.scraping_video, "wb") as f:
            f.write(b"video-data" * 100)

        # Pre-seed historical completed outputs to test false positive prevention
        self.sub_file = f"{self.tmpdir}/automation/subtitles/SNOS-411.srt"
        with open(self.sub_file, "w") as f:
            f.write("1\n00:00:01,000 --> 00:00:03,000\n旧字幕条目\n")

        self.repo_root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
        self.hook_script = os.path.join(self.repo_root, "scripts", "torrent_complete.py")

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        shutil.rmtree(self.tmpdir, ignore_errors=True)

    def run_extracted_tutorial(self, doc_type):
        if doc_type == "agent":
            doc_path = os.path.join(self.repo_root, "docs", "deployment", "agent-deployment.md")
            raw_script = extract_tutorial_bash_script(
                doc_path, "4. **端到端真实处理完整性验证指引", "### Phase 8"
            )
            # Map parameters to test environment
            script = raw_script.replace('"${HOME}/Development/scripts/torrent_complete.py"', f'"{self.hook_script}"')
            script = script.replace("{{HOST_PORT}}", str(self.port))
            script = script.replace("{{MEDIA_ROOT}}", self.tmpdir)
        elif doc_type == "macos":
            doc_path = os.path.join(self.repo_root, "docs", "deployment", "macos-deployment.md")
            raw_script = extract_tutorial_bash_script(
                doc_path, "### 级别 2：端到端真实媒体全链路验证"
            )
            # Map parameters to test environment
            script = raw_script.replace("/Users/your-username/Development/scripts/torrent_complete.py", self.hook_script)
            script = script.replace("4444", str(self.port))
            script = script.replace("/Volumes/External", self.tmpdir)
        else:
            raise ValueError(f"Unknown doc_type: {doc_type}")

        # Prepend fast sleep and environment mappings so test loop executes in milliseconds
        wrapper = f"""
        sleep() {{ builtin sleep 0.01; }}
        export MUSE_URL="http://127.0.0.1:{self.port}"
        export HOST_INPUT_PATH="{self.tmpdir}"
        export CONTAINER_INPUT_PATH="/data"

        {script}
        """

        return subprocess.run(
            ["bash", "-c", wrapper],
            capture_output=True,
            text=True,
            timeout=10
        )

    # ──────────────────────────────────────────────────────────────────────────
    # Tests directly executing extracted commands from docs/deployment/agent-deployment.md
    # ──────────────────────────────────────────────────────────────────────────

    def test_agent_tutorial_success_path(self):
        MockMuseDeploymentHandler.scenario = "success"
        proc = self.run_extracted_tutorial("agent")
        self.assertEqual(proc.returncode, 0, f"Expected success, got stderr: {proc.stderr}")
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("端到端全链路（ASR 转录与 LLM 翻译）部署验证通过", proc.stdout)

    def test_agent_tutorial_light_bypass_with_residual_sub_fails_asr_verification(self):
        """When existing subtitles trigger is_light bypass, residual subtitles must not cause false positive."""
        MockMuseDeploymentHandler.scenario = "light_bypass"
        proc = self.run_extracted_tutorial("agent")
        self.assertEqual(proc.returncode, 1, "Must fail ASR/LLM verification on light bypass")
        self.assertIn("未触发 ASR 转录与 LLM 翻译流水线", proc.stdout + proc.stderr)

    def test_agent_tutorial_preparation_failure(self):
        MockMuseDeploymentHandler.scenario = "prep_failed"
        proc = self.run_extracted_tutorial("agent")
        self.assertEqual(proc.returncode, 1)
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("Submission 准备阶段异常终止: 状态=failed", proc.stderr)

    def test_agent_tutorial_interrupted_submission(self):
        MockMuseDeploymentHandler.scenario = "interrupted"
        proc = self.run_extracted_tutorial("agent")
        self.assertEqual(proc.returncode, 1)
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("Submission 准备阶段异常终止: 状态=interrupted", proc.stderr)

    def test_agent_tutorial_ignored_submission(self):
        MockMuseDeploymentHandler.scenario = "ignored"
        proc = self.run_extracted_tutorial("agent")
        self.assertEqual(proc.returncode, 1)
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("Submission 准备阶段异常终止: 状态=ignored", proc.stderr)

    def test_agent_tutorial_unlinked_submission_timeout(self):
        MockMuseDeploymentHandler.scenario = "unlinked"
        proc = self.run_extracted_tutorial("agent")
        self.assertEqual(proc.returncode, 1)
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("超时未解析出关联 Job ID", proc.stderr)

    def test_agent_tutorial_query_500_failure(self):
        MockMuseDeploymentHandler.scenario = "query_500"
        proc = self.run_extracted_tutorial("agent")
        self.assertEqual(proc.returncode, 1)
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("查询 Submission 失败", proc.stderr)

    def test_agent_tutorial_job_execution_failure(self):
        MockMuseDeploymentHandler.scenario = "job_failed"
        proc = self.run_extracted_tutorial("agent")
        self.assertEqual(proc.returncode, 1)
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("关联 Job 执行失败: 状态=failed", proc.stderr)

    def test_agent_tutorial_missing_translated_file(self):
        MockMuseDeploymentHandler.scenario = "artifacts_missing"
        os.remove(self.sub_file)
        proc = self.run_extracted_tutorial("agent")
        self.assertEqual(proc.returncode, 1)
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("本次生成的字幕文件不存在或为空", proc.stderr)

    # ──────────────────────────────────────────────────────────────────────────
    # Tests directly executing extracted commands from docs/deployment/macos-deployment.md
    # ──────────────────────────────────────────────────────────────────────────

    def test_macos_tutorial_success_path(self):
        MockMuseDeploymentHandler.scenario = "success"
        proc = self.run_extracted_tutorial("macos")
        self.assertEqual(proc.returncode, 0, f"Expected success, got stderr: {proc.stderr}")
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("端到端全链路（ASR 转录与 LLM 翻译）部署验证通过", proc.stdout)

    def test_macos_tutorial_light_bypass_with_residual_sub_fails_asr_verification(self):
        MockMuseDeploymentHandler.scenario = "light_bypass"
        proc = self.run_extracted_tutorial("macos")
        self.assertEqual(proc.returncode, 1, "Must fail ASR/LLM verification on light bypass")
        self.assertIn("未触发 ASR 转录与 LLM 翻译流水线", proc.stdout + proc.stderr)

    def test_macos_tutorial_preparation_failure(self):
        MockMuseDeploymentHandler.scenario = "prep_failed"
        proc = self.run_extracted_tutorial("macos")
        self.assertEqual(proc.returncode, 1)
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("Submission 准备阶段异常终止: 状态=failed", proc.stderr)

    def test_macos_tutorial_interrupted_submission(self):
        MockMuseDeploymentHandler.scenario = "interrupted"
        proc = self.run_extracted_tutorial("macos")
        self.assertEqual(proc.returncode, 1)
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("Submission 准备阶段异常终止: 状态=interrupted", proc.stderr)

    def test_macos_tutorial_ignored_submission(self):
        MockMuseDeploymentHandler.scenario = "ignored"
        proc = self.run_extracted_tutorial("macos")
        self.assertEqual(proc.returncode, 1)
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("Submission 准备阶段异常终止: 状态=ignored", proc.stderr)

    def test_macos_tutorial_unlinked_submission_timeout(self):
        MockMuseDeploymentHandler.scenario = "unlinked"
        proc = self.run_extracted_tutorial("macos")
        self.assertEqual(proc.returncode, 1)
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("超时未解析出关联 Job ID", proc.stderr)

    def test_macos_tutorial_query_500_failure(self):
        MockMuseDeploymentHandler.scenario = "query_500"
        proc = self.run_extracted_tutorial("macos")
        self.assertEqual(proc.returncode, 1)
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("查询 Submission 接口失败", proc.stderr)

    def test_macos_tutorial_job_execution_failure(self):
        MockMuseDeploymentHandler.scenario = "job_failed"
        proc = self.run_extracted_tutorial("macos")
        self.assertEqual(proc.returncode, 1)
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("关联 Job 执行失败: 状态=failed", proc.stderr)

    def test_macos_tutorial_missing_translated_file(self):
        MockMuseDeploymentHandler.scenario = "artifacts_missing"
        os.remove(self.sub_file)
        proc = self.run_extracted_tutorial("macos")
        self.assertEqual(proc.returncode, 1)
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("本次生成的字幕文件不存在或为空", proc.stderr)


if __name__ == "__main__":
    unittest.main()
