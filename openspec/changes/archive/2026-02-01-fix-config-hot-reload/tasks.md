# Tasks: fix-config-hot-reload

## 1. Implementation

- [x] 1.1 Update `processor.Service` struct to hold `*config.Manager` instead of `*config.Config`
- [x] 1.2 Update `processor.New()` to accept `*config.Manager` parameter
- [x] 1.3 Update `processor.Process()` to get fresh config and recreate executors at job start
- [x] 1.4 Update `main.go` to pass config manager to processor instead of config snapshot

## 2. Verification

- [x] 2.1 Build and verify no compilation errors
- [x] 2.2 Test: change `translate.model` while container running, verify new job uses new model
