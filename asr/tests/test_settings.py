"""Environment settings: defaults, overrides and the errors an operator can make."""

import unittest

from asr.settings import (
    DEFAULT_ALIGNER_MODEL,
    DEFAULT_MODEL,
    ConfigError,
    load_settings,
)


class DefaultsTest(unittest.TestCase):
    def test_defaults_are_the_cpu_route(self):
        settings = load_settings({})
        self.assertEqual(settings.engine, "whisper")
        self.assertEqual(settings.model, DEFAULT_MODEL["whisper"])
        self.assertEqual(settings.host, "0.0.0.0")
        self.assertEqual(settings.port, 8766)
        self.assertEqual(settings.model_dir, "/models")
        self.assertEqual(settings.language, "")
        self.assertEqual(settings.host_media_path, "")
        self.assertEqual(settings.device, "cpu")
        self.assertEqual(settings.compute_type, "int8")
        self.assertEqual(settings.vad_filter, True)
        self.assertEqual(settings.version, "dev")
        self.assertEqual(settings.ffmpeg, "ffmpeg")

    def test_the_qwen_route_gets_its_own_default_model(self):
        settings = load_settings({"ASR_ENGINE": "qwen"})
        self.assertEqual(settings.model, DEFAULT_MODEL["qwen"])
        self.assertEqual(settings.aligner_model, DEFAULT_ALIGNER_MODEL)
        self.assertEqual(settings.device_map, "cuda:0")
        self.assertEqual(settings.dtype, "bfloat16")
        self.assertEqual(settings.max_new_tokens, 4096)
        self.assertEqual(settings.max_batch_size, 8)


class OverrideTest(unittest.TestCase):
    def test_every_value_can_be_set_from_the_environment(self):
        settings = load_settings(
            {
                "ASR_ENGINE": "Qwen",
                "ASR_MODEL": "Qwen/Qwen3-ASR-0.6B",
                "ASR_LANGUAGE": "zh",
                "ASR_HOST": "127.0.0.1",
                "ASR_PORT": "9000",
                "HOST_MEDIA_PATH": "/mnt/media",
                "ASR_MODEL_DIR": "/cache",
                "ASR_VERSION": "1.2.3",
                "ASR_FFMPEG": "/opt/ffmpeg",
                "ASR_ALIGNER_MODEL": "",
                "ASR_DEVICE_MAP": "cuda:1",
                "ASR_DTYPE": "float16",
                "ASR_MAX_NEW_TOKENS": "1024",
                "ASR_MAX_BATCH_SIZE": "2",
            }
        )
        self.assertEqual(settings.engine, "qwen")
        self.assertEqual(settings.model, "Qwen/Qwen3-ASR-0.6B")
        self.assertEqual(settings.language, "zh")
        self.assertEqual(settings.host, "127.0.0.1")
        self.assertEqual(settings.port, 9000)
        self.assertEqual(settings.host_media_path, "/mnt/media")
        self.assertEqual(settings.model_dir, "/cache")
        self.assertEqual(settings.version, "1.2.3")
        self.assertEqual(settings.ffmpeg, "/opt/ffmpeg")
        self.assertEqual(settings.aligner_model, "")
        self.assertEqual(settings.device_map, "cuda:1")
        self.assertEqual(settings.dtype, "float16")
        self.assertEqual(settings.max_new_tokens, 1024)
        self.assertEqual(settings.max_batch_size, 2)

    def test_whisper_values_can_be_set_from_the_environment(self):
        settings = load_settings(
            {
                "ASR_DEVICE": "cpu",
                "ASR_COMPUTE_TYPE": "float32",
                "ASR_CPU_THREADS": "4",
                "ASR_BEAM_SIZE": "1",
                "ASR_VAD_FILTER": "false",
            }
        )
        self.assertEqual(settings.compute_type, "float32")
        self.assertEqual(settings.cpu_threads, 4)
        self.assertEqual(settings.beam_size, 1)
        self.assertFalse(settings.vad_filter)

    def test_boolean_spellings(self):
        for value in ("1", "true", "yes", "on"):
            with self.subTest(value=value):
                self.assertTrue(load_settings({"ASR_VAD_FILTER": value}).vad_filter)
        for value in ("0", "false", "no", "off"):
            with self.subTest(value=value):
                self.assertFalse(load_settings({"ASR_VAD_FILTER": value}).vad_filter)


class InvalidValueTest(unittest.TestCase):
    def test_an_unknown_engine_is_rejected(self):
        with self.assertRaises(ConfigError) as caught:
            load_settings({"ASR_ENGINE": "videocaptioner"})
        self.assertIn("ASR_ENGINE", str(caught.exception))

    def test_an_unknown_dtype_is_rejected(self):
        with self.assertRaises(ConfigError) as caught:
            load_settings({"ASR_DTYPE": "float8"})
        self.assertIn("ASR_DTYPE", str(caught.exception))

    def test_a_bad_number_is_rejected(self):
        with self.assertRaises(ConfigError):
            load_settings({"ASR_PORT": "eight"})
        with self.assertRaises(ConfigError):
            load_settings({"ASR_BEAM_SIZE": "0"})
        with self.assertRaises(ConfigError):
            load_settings({"ASR_CPU_THREADS": "-1"})

    def test_a_bad_boolean_is_rejected(self):
        with self.assertRaises(ConfigError) as caught:
            load_settings({"ASR_VAD_FILTER": "maybe"})
        self.assertIn("ASR_VAD_FILTER", str(caught.exception))


class SummaryTest(unittest.TestCase):
    def test_the_summary_names_only_the_fields_of_the_running_engine(self):
        whisper = load_settings({}).summary()
        self.assertEqual(whisper["engine"], "whisper")
        self.assertEqual(whisper["language"], "auto")
        self.assertIn("compute_type", whisper)
        self.assertNotIn("device_map", whisper)

        qwen = load_settings({"ASR_ENGINE": "qwen"}).summary()
        self.assertIn("device_map", qwen)
        self.assertIn("aligner_model", qwen)
        self.assertNotIn("compute_type", qwen)


if __name__ == "__main__":
    unittest.main()
