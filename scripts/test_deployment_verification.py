import http.server
import json
import os
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
            if MockMuseDeploymentHandler.scenario in ("success", "job_failed", "artifacts_missing"):
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
                        "error": "LLM API key invalid",
                        "job": {"translated_path": ""}
                    }
                else:
                    if MockMuseDeploymentHandler.job_polls < 2:
                        resp = {
                            "id": "job-associated-999",
                            "status": "processing",
                            "stage": "transcribing",
                            "error": "",
                            "job": {"translated_path": ""}
                        }
                    else:
                        resp = {
                            "id": "job-associated-999",
                            "status": "completed",
                            "stage": "translating",
                            "error": "",
                            "job": {"translated_path": "/data/automation/subtitles/SNOS-411.srt"}
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

        # Pre-seed historical completed outputs to test false positive prevention
        self.old_video = f"{self.tmpdir}/automation/scraping/SNOS-411.mp4"
        with open(self.old_video, "wb") as f:
            f.write(b"old-video-data" * 100)
        self.old_sub = f"{self.tmpdir}/automation/subtitles/SNOS-411.srt"
        with open(self.old_sub, "w") as f:
            f.write("1\n00:00:01,000 --> 00:00:03,000\n旧字幕条目\n")

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        shutil.rmtree(self.tmpdir, ignore_errors=True)

    def run_agent_verification_script(self):
        repo_root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
        hook_script = os.path.join(repo_root, "scripts", "torrent_complete.py")

        bash_script = f"""
        set -e
        export MUSE_URL="http://127.0.0.1:{self.port}"
        export HOST_INPUT_PATH="{self.tmpdir}"
        export CONTAINER_INPUT_PATH="/data"

        OUTPUT=$(python3 "{hook_script}" \
          "SNOS-411" "whisparr" "real-hash-001" "{self.sample_file}" 2>&1)
        EXIT_CODE=$?
        if [ ${{EXIT_CODE}} -ne 0 ]; then
          echo "❌ Webhook 投递执行失败 (退出码 ${{EXIT_CODE}}): ${{OUTPUT}}" >&2
          exit 1
        fi

        SUBMISSION_ID=$(echo "${{OUTPUT}}" | grep -o 'submission_id=[^ ]*' | head -n 1 | cut -d= -f2)
        if [ -z "${{SUBMISSION_ID}}" ]; then
          echo "❌ 无法从 Webhook 输出中解析 submission_id: ${{OUTPUT}}" >&2
          exit 1
        fi

        JOB_ID=""
        for i in $(seq 1 10); do
          if ! SUB_RESP=$(curl -fsS "http://127.0.0.1:{self.port}/api/v1/tasks/submission/${{SUBMISSION_ID}}" 2>/dev/null); then
            echo "❌ 查询 Submission 失败: 无法访问接口" >&2
            exit 1
          fi

          SUB_STATUS=$(echo "${{SUB_RESP}}" | jq -r '.status // empty')
          SUB_ERROR=$(echo "${{SUB_RESP}}" | jq -r '.error // empty')
          JOB_ID=$(echo "${{SUB_RESP}}" | jq -r '.canonical_task.id // empty')

          if [ "${{SUB_STATUS}}" = "failed" ] || [ "${{SUB_STATUS}}" = "ignored" ] || [ "${{SUB_STATUS}}" = "interrupted" ]; then
            echo "❌ Submission 准备阶段异常终止: 状态=${{SUB_STATUS}}, 错误=${{SUB_ERROR}}" >&2
            exit 1
          fi

          if [ -n "${{JOB_ID}}" ]; then
            break
          fi

          sleep 0.05
        done

        if [ -z "${{JOB_ID}}" ]; then
          echo "❌ 超时未解析出关联 Job ID (canonical_task 为空)，终止验证" >&2
          exit 1
        fi

        STATUS=""
        for i in $(seq 1 10); do
          if ! TASK_JSON=$(curl -fsS "http://127.0.0.1:{self.port}/api/v1/tasks/job/${{JOB_ID}}" 2>/dev/null); then
            echo "❌ 查询关联 Job 失败 (Job ID: ${{JOB_ID}})" >&2
            exit 1
          fi

          STATUS=$(echo "${{TASK_JSON}}" | jq -r '.status // empty')
          STAGE=$(echo "${{TASK_JSON}}" | jq -r '.stage // empty')
          ERROR=$(echo "${{TASK_JSON}}" | jq -r '.error // empty')

          if [ "${{STATUS}}" = "failed" ] || [ "${{STATUS}}" = "interrupted" ]; then
            echo "❌ 关联 Job 执行失败: 状态=${{STATUS}}, 阶段=${{STAGE}}, 错误=${{ERROR}}" >&2
            exit 1
          fi

          if [ "${{STATUS}}" = "completed" ]; then
            break
          fi

          sleep 0.05
        done

        if [ "${{STATUS}}" != "completed" ]; then
          echo "❌ 关联 Job 未能在超时时间内达到 completed 状态 (当前状态: ${{STATUS}})" >&2
          exit 1
        fi

        if [ ! -s "{self.tmpdir}/automation/scraping/SNOS-411.mp4" ]; then
          echo "❌ 待刮削视频文件缺失或为空" >&2
          exit 1
        fi

        if [ ! -s "{self.tmpdir}/automation/subtitles/SNOS-411.srt" ]; then
          echo "❌ 中文字幕文件缺失或为空" >&2
          exit 1
        fi

        echo "✅ 端到端部署全链路验证通过: Submission=${{SUBMISSION_ID}}, Job=${{JOB_ID}}"
        exit 0
        """

        return subprocess.run(
            ["bash", "-c", bash_script],
            capture_output=True,
            text=True,
            timeout=10
        )

    def test_success_path_correlates_job_and_verifies_artifacts(self):
        MockMuseDeploymentHandler.scenario = "success"
        proc = self.run_agent_verification_script()
        self.assertEqual(proc.returncode, 0, f"Expected success, got stderr: {proc.stderr}")
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("端到端部署全链路验证通过", proc.stdout)

    def test_preparation_failure_terminates_and_never_reads_historical_job(self):
        MockMuseDeploymentHandler.scenario = "prep_failed"
        proc = self.run_agent_verification_script()
        self.assertEqual(proc.returncode, 1)
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed, "Must not access historical SNOS-411 job")
        self.assertIn("Submission 准备阶段异常终止: 状态=failed", proc.stderr)

    def test_interrupted_submission_terminates_explicitly(self):
        MockMuseDeploymentHandler.scenario = "interrupted"
        proc = self.run_agent_verification_script()
        self.assertEqual(proc.returncode, 1)
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("Submission 准备阶段异常终止: 状态=interrupted", proc.stderr)

    def test_ignored_submission_terminates_explicitly(self):
        MockMuseDeploymentHandler.scenario = "ignored"
        proc = self.run_agent_verification_script()
        self.assertEqual(proc.returncode, 1)
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("Submission 准备阶段异常终止: 状态=ignored", proc.stderr)

    def test_unlinked_submission_timeouts_and_fails(self):
        MockMuseDeploymentHandler.scenario = "unlinked"
        proc = self.run_agent_verification_script()
        self.assertEqual(proc.returncode, 1)
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("超时未解析出关联 Job ID", proc.stderr)

    def test_submission_query_failure_terminates_explicitly(self):
        MockMuseDeploymentHandler.scenario = "query_500"
        proc = self.run_agent_verification_script()
        self.assertEqual(proc.returncode, 1)
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("查询 Submission 失败", proc.stderr)

    def test_job_execution_failure_terminates_explicitly(self):
        MockMuseDeploymentHandler.scenario = "job_failed"
        proc = self.run_agent_verification_script()
        self.assertEqual(proc.returncode, 1)
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("关联 Job 执行失败: 状态=failed", proc.stderr)

    def test_missing_artifacts_causes_failure(self):
        MockMuseDeploymentHandler.scenario = "artifacts_missing"
        os.remove(self.old_sub)
        proc = self.run_agent_verification_script()
        self.assertEqual(proc.returncode, 1)
        self.assertFalse(MockMuseDeploymentHandler.history_job_accessed)
        self.assertIn("中文字幕文件缺失或为空", proc.stderr)


if __name__ == "__main__":
    unittest.main()
