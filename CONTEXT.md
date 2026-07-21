# Fusionn-Muse

Fusionn-Muse turns completed media downloads into processed videos with translated subtitles.

## Language

**Job**:
One media item accepted into the subtitle-processing pipeline, including all retry attempts. A Job retains the same Job ID throughout its lifecycle.
_Avoid_: Trace, request

**Job ID**:
The globally unique correlation identifier for all activity belonging to one Job.
_Avoid_: Trace ID

**Attempt**:
One execution of a Job. Automatic retries create new Attempts while retaining the Job ID.
_Avoid_: Retry as the name for the initial execution

**Manual requeue**:
A manually requested return of media to processing, creating a new Job with a new Job ID.
_Avoid_: Attempt, manual retry
