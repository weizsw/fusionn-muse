---
name: Bug report
about: Report a problem with subtitle processing and provide context to reproduce it.
---

## Steps to reproduce

Describe the input and list the steps that trigger the problem.

## Actual behavior

What happened?

## Expected behavior

What did you expect to happen?

## Stage reached

Which stage did the affected execution reach?
Choose preparing, moving, transcribing, translating, delivering, or not sure.

## Video code or Job ID

Provide the affected Video code or Job ID.
If preparation failed before a Job exists, explain that and provide a Submission ID if available.
If you cannot find these identifiers, say so rather than guess.

## Burned-in subtitles

Does the source video contain subtitles embedded in the picture (burned-in subtitles)?
Answer yes, no, or not sure.

## Logs or error line

Paste a log excerpt or the affected Attempt's error line below.
An Attempt is one execution of a Job.
A Job ID correlates logs across Attempts, not just one Attempt.
Include logs from the affected execution, with timestamps or an Attempt number if available.
For preparation before a Job exists, look for `submission=<Submission ID>` in the logs.
Remove secrets and personal information before posting.

```text
Paste logs or the error line here.
```
