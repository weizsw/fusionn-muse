# Fusionn-Muse

Fusionn-Muse turns completed media downloads into processed videos with translated subtitles.

## Language

**Job**:
One Media item accepted into the subtitle-processing pipeline, including all Attempts. A Job retains the same Job ID throughout its lifecycle.
_Avoid_: Trace, request

**Job ID**:
The globally unique correlation identifier for all activity belonging to one Job.
_Avoid_: Trace ID

**Attempt**:
One execution of a Job. Manual retries, Translation retries, Retranslations, and Resumes are later Attempts that retain the Job ID.
_Avoid_: Retry as the name for the initial execution

**Manual retry**:
A manually requested Attempt for a terminally failed Job that starts at the first incomplete stage.
_Avoid_: Manual requeue, new Job, Retranslation

**Media item**:
A source media file identified primarily by a strong sampled-content signature, with the normalized filename as a fallback source key when content cannot be read. Different paths and filenames can identify the same Media item; the same normalized filename can identify different Media items when their signatures differ.
_Avoid_: Job, path identity, filename-only identity

**Retranslation**:
A manually requested Attempt that starts at translation using a Job's persisted Transcription.
_Avoid_: Manual retry, Retranscription

**Duplicate submission**:
A request to enqueue a Media item already owned by an existing Job. It always resolves to that Job and never creates another Job.
_Avoid_: Manual retry, new Job

**Translation retry**:
An automatic Attempt that resumes at translation using the Job's persisted Transcription.
_Avoid_: Retranscription, Manual retry

**Transcription**:
A durable source-language subtitle artifact produced from a Media item and used as the input to Translation retries and Retranslations.
_Avoid_: Translation, temporary subtitle

**Interrupted Job**:
A Job whose Attempt was running when the service stopped without recording a final outcome. It requires explicit operator recovery.
_Avoid_: Queued Job, automatically resumed Job, successful Job

**Resume**:
A manually requested Attempt for an Interrupted Job that starts at its first incomplete stage while retaining the Job ID.
_Avoid_: Manual retry, automatic retry

**Stage checkpoint**:
Durable evidence that a Job stage completed successfully.
_Avoid_: Attempt, temporary output

**Queue**:
The durable set of Attempts waiting to start.
_Avoid_: Active Job, Job history

**Active Attempt**:
An Attempt that is queued or running.
_Avoid_: Active Job, completed Attempt

**Terminal Job**:
A Job whose ordinary processing outcome is completed or failed.
_Avoid_: Interrupted Job, Active Attempt

**Sufficient Chinese subtitle coverage**:
Existing Chinese subtitle-like text present in separated portions of a video's main content, enough to make transcription and translation unnecessary. Text confined to one portion does not qualify.
_Avoid_: Chinese text detected
