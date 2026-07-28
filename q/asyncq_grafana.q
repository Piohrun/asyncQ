/
AsyncQ Grafana helper protocol.

Load this file in a kdb+ process or, preferably, in a gateway that fronts worker
processes. The reference async implementation evaluates jobs in-process and is
therefore a protocol baseline, not a production worker-pool scheduler.

Streaming is push-oriented: `.grafana.asyncq.stream.start` stores the Grafana
IPC handle for a stream ID, and q code can call `.grafana.asyncq.stream.publish`
whenever new rows are ready.

Terminal jobs are retained for one hour, with at most 1000 jobs retained in
total. Active streams are capped at 256. These conservative process-local
limits can be tuned before serving requests. Panopticon request functions must
be fully qualified names present in TRUSTED_PANOPTICON_FUNCTIONS; arbitrary
per-query function expressions are rejected. Request and stream IDs must be
non-empty char vectors made only from printable ASCII graphic characters
(bytes 33 through 126) and no longer than ID_MAX_CHARS; invalid inputs are
rejected before retention or registry state is changed.
\

.grafana.asyncq.JOB_RETENTION:0D01:00:00.000000000;
.grafana.asyncq.MAX_RETAINED_JOBS:1000;
.grafana.asyncq.MAX_ACTIVE_STREAMS:256;
.grafana.asyncq.ID_MAX_CHARS:128;
.grafana.asyncq.PANOPTICON_FUNCTION_NAME_MAX_CHARS:256;
.grafana.asyncq.TRUSTED_PANOPTICON_FUNCTIONS:`symbol$();
.grafana.asyncq.TERMINAL_JOB_STATUSES:("done";"error";"cancelled");
.grafana.asyncq.NAME_START_CHARS:"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ";
.grafana.asyncq.NAME_CHARS:.grafana.asyncq.NAME_START_CHARS,"_0123456789";
.grafana.asyncq.RESERVED_NAMESPACE_ROOTS:("q";"Q";"h";"j";"z";"kx";"m");

.grafana.asyncq.JOBS:([] jobId:(); status:(); progress:`float$(); result:(); error:(); message:(); errorClass:(); stackTrace:(); request:(); started:`timestamp$(); finished:`timestamp$(); worker:(); resultType:());
.grafana.asyncq.STREAMS:([] streamId:(); handle:`int$(); request:(); seq:`long$(); started:`timestamp$());

.grafana.asyncq.util.get:{[d;k;default]
    $[k in key d; d k; default]
  };

.grafana.asyncq.util.text:{[cell]
    $[0=type cell; $[0=count cell; ""; .grafana.asyncq.util.text first cell];
      10h=type cell; cell;
      -10h=type cell; enlist cell;
      string cell]
  };

.grafana.asyncq.util.matchText:{[cell;target]
    (.grafana.asyncq.util.text cell)~.grafana.asyncq.util.text target
  };

.grafana.asyncq.util.integerAtom:{[x]
    (type x) in -5 -6 -7h
  };

.grafana.asyncq.util.jobLimit:{
    limit:.grafana.asyncq.MAX_RETAINED_JOBS;
    if[not .grafana.asyncq.util.integerAtom limit; '"MAX_RETAINED_JOBS must be a positive integer atom"];
    if[1>limit; '"MAX_RETAINED_JOBS must be a positive integer atom"];
    limit
  };

.grafana.asyncq.util.streamLimit:{
    limit:.grafana.asyncq.MAX_ACTIVE_STREAMS;
    if[not .grafana.asyncq.util.integerAtom limit; '"MAX_ACTIVE_STREAMS must be a positive integer atom"];
    if[1>limit; '"MAX_ACTIVE_STREAMS must be a positive integer atom"];
    limit
  };

.grafana.asyncq.util.idLimit:{
    limit:.grafana.asyncq.ID_MAX_CHARS;
    if[not .grafana.asyncq.util.integerAtom limit; '"ID_MAX_CHARS must be a positive integer atom"];
    if[1>limit; '"ID_MAX_CHARS must be a positive integer atom"];
    limit
  };

.grafana.asyncq.util.panopticonFunctionNameLimit:{
    limit:.grafana.asyncq.PANOPTICON_FUNCTION_NAME_MAX_CHARS;
    if[not .grafana.asyncq.util.integerAtom limit; '"PANOPTICON_FUNCTION_NAME_MAX_CHARS must be a positive integer atom"];
    if[1>limit; '"PANOPTICON_FUNCTION_NAME_MAX_CHARS must be a positive integer atom"];
    limit
  };

/ Return true only when every byte is an ASCII graphic character (`!` through `~`).
.grafana.asyncq.util.graphicAscii:{[cell]
    allowed:"c"$33+til 94;
    all cell in allowed
  };

/ Validate an external ID without formatting or casting external data.
/ params:  cell  - external ID value
/          label - trusted field label used in errors
/ returns: unchanged non-empty, graphic-ASCII char vector no longer than ID_MAX_CHARS.
.grafana.asyncq.util.normalizeId:{[cell;label]
    if[10h<>type cell; 'label," must be a char vector"];
    if[0=count cell; 'label," must not be empty"];
    limit:.grafana.asyncq.util.idLimit[];
    if[limit<count cell; 'label," exceeds ID_MAX_CHARS"];
    if[not .grafana.asyncq.util.graphicAscii cell; 'label," must contain only printable ASCII graphic characters (bytes 33..126)"];
    cell
  };

.grafana.asyncq.util.normalizeJobId:{[jobId]
    .grafana.asyncq.util.normalizeId[jobId;"job ID"]
  };

.grafana.asyncq.util.normalizeStreamId:{[streamId]
    .grafana.asyncq.util.normalizeId[streamId;"stream ID"]
  };

.grafana.asyncq.util.byJobId:{[jobId]
    where .grafana.asyncq.util.matchText[;jobId] each .grafana.asyncq.JOBS`jobId
  };

.grafana.asyncq.util.byStreamId:{[streamId]
    where .grafana.asyncq.util.matchText[;streamId] each .grafana.asyncq.STREAMS`streamId
  };

.grafana.asyncq.util.isTerminalJobStatus:{[status]
    .grafana.asyncq.util.text[status] in .grafana.asyncq.TERMINAL_JOB_STATUSES
  };

/ Remove expired terminal jobs and enforce the retained-job count bound.
/ params:  reserve - number of free slots required by the caller
/ returns: generic null
/ side effects: clears terminal requests and removes terminal rows only.
.grafana.asyncq.util.cleanupJobs:{[reserve]
    limit:.grafana.asyncq.util.jobLimit[];
    if[not .grafana.asyncq.util.integerAtom reserve; '"job cleanup reserve must be a non-negative integer atom"];
    if[0>reserve; '"job cleanup reserve must be a non-negative integer atom"];
    if[limit<reserve; '"job capacity is smaller than the requested reservation"];
    retention:.grafana.asyncq.JOB_RETENTION;
    if[-16h<>type retention; '"JOB_RETENTION must be a non-negative timespan atom"];
    if[0D00:00:00.000000000>retention; '"JOB_RETENTION must be a non-negative timespan atom"];

    terminalMask:.grafana.asyncq.util.isTerminalJobStatus each .grafana.asyncq.JOBS`status;
    terminalRows:where terminalMask;
    if[count terminalRows;
      .grafana.asyncq.JOBS::update request:(::) from .grafana.asyncq.JOBS where i in terminalRows];

    terminalAt:?[null .grafana.asyncq.JOBS`finished; .grafana.asyncq.JOBS`started; .grafana.asyncq.JOBS`finished];
    cutoff:.z.p-retention;
    expiredRows:where terminalMask & (not null terminalAt) & terminalAt<cutoff;
    if[count expiredRows;
      .grafana.asyncq.JOBS::delete from .grafana.asyncq.JOBS where i in expiredRows];

    target:limit-reserve;
    excess:(count .grafana.asyncq.JOBS)-target;
    if[0>=excess; :(::)];

    terminalMask:.grafana.asyncq.util.isTerminalJobStatus each .grafana.asyncq.JOBS`status;
    terminalRows:where terminalMask;
    terminalAt:?[null .grafana.asyncq.JOBS`finished; .grafana.asyncq.JOBS`started; .grafana.asyncq.JOBS`finished];
    orderedRows:terminalRows iasc terminalAt[terminalRows];
    evictCount:excess&count orderedRows;
    evictRows:evictCount#orderedRows;
    if[count evictRows;
      .grafana.asyncq.JOBS::delete from .grafana.asyncq.JOBS where i in evictRows];
    if[excess>evictCount; '"job capacity exhausted: retained live jobs fill MAX_RETAINED_JOBS"];
    (::)
  };

.grafana.asyncq.util.requireSingleJobRow:{[jobId;rows]
    if[1<count rows; '"duplicate retained job id: ",jobId];
    if[0=count rows; '"job not found"];
    first select from .grafana.asyncq.JOBS where i=first rows
  };

.grafana.asyncq.util.requireSingleStreamRow:{[streamId;rows]
    if[1<count rows; '"duplicate active stream id: ",streamId];
    if[0=count rows; '"stream not found"];
    first select from .grafana.asyncq.STREAMS where i=first rows
  };

.grafana.asyncq.util.worker:{string system "p"};

.grafana.asyncq.util.describe:{[x]
    "type=",string[type x],";count=",string count x
  };

.grafana.asyncq.util.errorInfo:{[err;bt]
    msg:.grafana.asyncq.util.text err;
    stack:@[{.Q.sbt x}; bt; {[trapErr] ""}];
    `Error`Message`ErrorClass`StackTrace!(msg;msg;"q";stack)
  };

.grafana.asyncq.util.trapEval:{[req]
    .Q.trp[
      {[x] (1b; .grafana.asyncq.util.evalQuery x)};
      req;
      {[err;bt] (0b; .grafana.asyncq.util.errorInfo[err;bt])}
      ]
  };

.grafana.asyncq.util.statusDict:{[d]
    `JobID`Status`Progress`Error`Message`ErrorClass`StackTrace`Worker`Started`Finished`ResultType!(
      .grafana.asyncq.util.text .grafana.asyncq.util.get[d;`JobID;""];
      .grafana.asyncq.util.text .grafana.asyncq.util.get[d;`Status;""];
      .grafana.asyncq.util.get[d;`Progress;0f];
      .grafana.asyncq.util.text .grafana.asyncq.util.get[d;`Error;""];
      .grafana.asyncq.util.text .grafana.asyncq.util.get[d;`Message;""];
      .grafana.asyncq.util.text .grafana.asyncq.util.get[d;`ErrorClass;""];
      .grafana.asyncq.util.text .grafana.asyncq.util.get[d;`StackTrace;""];
      .grafana.asyncq.util.text .grafana.asyncq.util.get[d;`Worker;.grafana.asyncq.util.worker[]];
      .grafana.asyncq.util.get[d;`Started;0Np];
      .grafana.asyncq.util.get[d;`Finished;0Np];
      .grafana.asyncq.util.text .grafana.asyncq.util.get[d;`ResultType;""])
  };

.grafana.asyncq.util.statusFromRow:{[r]
    .grafana.asyncq.util.statusDict `JobID`Status`Progress`Error`Message`ErrorClass`StackTrace`Worker`Started`Finished`ResultType!(r`jobId; r`status; r`progress; r`error; r`message; r`errorClass; r`stackTrace; r`worker; r`started; r`finished; r`resultType)
  };

.grafana.asyncq.util.streamDict:{[streamId;status;seq;payload;err]
    errText:.grafana.asyncq.util.text err;
    `MessageType`StreamID`Seq`Payload`Error`Message`ErrorClass`StackTrace`Worker!(
      status;
      streamId;
      seq;
      payload;
      errText;
      errText;
      "";
      "";
      .grafana.asyncq.util.worker[])
  };

.grafana.asyncq.util.validNamePart:{[part]
    if[0=count part; :0b];
    if[not first[part] in .grafana.asyncq.NAME_START_CHARS; :0b];
    all part in .grafana.asyncq.NAME_CHARS
  };

.grafana.asyncq.util.validPanopticonFunctionName:{[name]
    if[10h<>type name; :0b];
    if[2>count name; :0b];
    if[.grafana.asyncq.util.panopticonFunctionNameLimit[]<count name; :0b];
    if[not "."~first name; :0b];
    parts:"." vs 1_name;
    if[2>count parts; :0b];
    if[any first[parts]~/:.grafana.asyncq.RESERVED_NAMESPACE_ROOTS; :0b];
    all .grafana.asyncq.util.validNamePart each parts
  };

/ Resolve an explicitly trusted, fully qualified Panopticon request function.
/ params:  name - char-vector function name supplied in the request
/ returns: an already-interned symbol from TRUSTED_PANOPTICON_FUNCTIONS
/ note:    external text is compared with trusted symbols and is never cast.
.grafana.asyncq.util.resolvePanopticonFunction:{[name]
    if[10h<>type name; '"PanopticonRequestFunction must be a char-vector function name"];
    if[not .grafana.asyncq.util.validPanopticonFunctionName name; '"PanopticonRequestFunction must be a bounded, fully qualified non-reserved function name"];
    trusted:.grafana.asyncq.TRUSTED_PANOPTICON_FUNCTIONS;
    if[-11h=type trusted; trusted:enlist trusted];
    if[11h<>type trusted; '"TRUSTED_PANOPTICON_FUNCTIONS must be a symbol vector"];
    matches:where name~/:string trusted;
    if[0=count matches; '"PanopticonRequestFunction is not trusted"];
    if[1<count matches; '"PanopticonRequestFunction appears more than once in the trusted list"];
    fn:trusted first matches;
    if[100h>type get fn; '"trusted Panopticon request name does not resolve to a function"];
    fn
  };

.grafana.asyncq.util.evalQuery:{[req]
    req:$[98h=type req; first req; req];
    if[99h<>type req; '"async request must be a dictionary"];
    qd:.grafana.asyncq.util.get[req;`Query;(::)];
    if[99h<>type qd; '"async request Query must be a dictionary"];
    fn:.grafana.asyncq.util.get[qd;`PanopticonRequestFunction;""];
    if[not ""~fn;
      trustedFn:.grafana.asyncq.util.resolvePanopticonFunction fn;
      :reval (trustedFn;req)];
    query:.grafana.asyncq.util.get[qd;`Query;(::)];
    if[10h<>type query; '"async request Query.Query must be a char vector"];
    reval parse query
  };

/ Submit an async query.
/ params:  req - dictionary sent by the Grafana backend; Query text or Panopticon request function is evaluated.
/ returns: status dictionary with JobID, Status, Progress, Error.
/ note:    this reference evaluates synchronously but preserves the queued submit response for protocol compatibility.
.grafana.asyncq.async.submit:{[req]
    if[99h<>type req; '"async submit request must be a dictionary"];
    jobId:.grafana.asyncq.util.normalizeJobId .grafana.asyncq.util.get[req;`RequestID;""];
    .grafana.asyncq.util.cleanupJobs 0;
    rows:.grafana.asyncq.util.byJobId jobId;
    if[1<count rows; '"duplicate retained job id: ",jobId];
    if[1=count rows;
      :.grafana.asyncq.util.statusFromRow first select from .grafana.asyncq.JOBS where i=first rows];
    .grafana.asyncq.util.cleanupJobs 1;
    started:.z.p;
    worker:.grafana.asyncq.util.worker[];
    .grafana.asyncq.JOBS::.grafana.asyncq.JOBS,enlist `jobId`status`progress`result`error`message`errorClass`stackTrace`request`started`finished`worker`resultType!(enlist jobId;enlist "running";0f;(::);enlist "";enlist "";enlist "";enlist "";req;started;0Np;enlist worker;enlist "");

    trapped:.grafana.asyncq.util.trapEval req;
    ok:first trapped;
    payload:last trapped;
    rows:.grafana.asyncq.util.byJobId jobId;
    if[1<>count rows; '"retained job id became ambiguous during execution"];
    idx:first rows;

    $[ok;
        .grafana.asyncq.JOBS::update status:enlist "done", progress:1f, result:enlist payload, error:enlist "", message:enlist "", errorClass:enlist "", stackTrace:enlist "", request:(::), finished:.z.p, resultType:enlist .grafana.asyncq.util.describe payload from .grafana.asyncq.JOBS where i=idx;
        .grafana.asyncq.JOBS::update status:enlist "error", progress:1f, result:enlist (::), error:enlist payload`Error, message:enlist payload`Message, errorClass:enlist payload`ErrorClass, stackTrace:enlist payload`StackTrace, request:(::), finished:.z.p, resultType:enlist "" from .grafana.asyncq.JOBS where i=idx
      ];
    .grafana.asyncq.util.statusDict `JobID`Status`Progress`Error`Message`ErrorClass`StackTrace`Worker`Started`Finished`ResultType!(jobId; "queued"; 0f; ""; ""; ""; ""; worker; started; 0Np; "")
  };

/ Return async query status.
/ params:  jobId - char vector job ID returned by async.submit.
/ returns: status dictionary.
.grafana.asyncq.async.status:{[jobId]
    jobId:.grafana.asyncq.util.normalizeJobId jobId;
    .grafana.asyncq.util.cleanupJobs 0;
    rows:.grafana.asyncq.util.byJobId jobId;
    r:.grafana.asyncq.util.requireSingleJobRow[jobId;rows];
    .grafana.asyncq.util.statusFromRow r
  };

/ Return async query result.
/ params:  jobId - char vector job ID returned by async.submit.
/ returns: table or grouped table.
.grafana.asyncq.async.result:{[jobId]
    jobId:.grafana.asyncq.util.normalizeJobId jobId;
    .grafana.asyncq.util.cleanupJobs 0;
    rows:.grafana.asyncq.util.byJobId jobId;
    r:.grafana.asyncq.util.requireSingleJobRow[jobId;rows];
    if[not r[`status]~"done"; '"job not done"];
    r`result
  };

/ Cancel an async query best-effort.
/ params:  jobId - char vector job ID returned by async.submit.
/ returns: status dictionary.
.grafana.asyncq.async.cancel:{[jobId]
    jobId:.grafana.asyncq.util.normalizeJobId jobId;
    .grafana.asyncq.util.cleanupJobs 0;
    rows:.grafana.asyncq.util.byJobId jobId;
    if[0=count rows; :.grafana.asyncq.util.statusDict `JobID`Status`Progress`Error`Message`ErrorClass`StackTrace`Worker`Started`Finished`ResultType!(jobId; "missing"; 0f; "job not found"; "job not found"; "missing"; ""; .grafana.asyncq.util.worker[]; 0Np; 0Np; "")];
    r:.grafana.asyncq.util.requireSingleJobRow[jobId;rows];
    if[not .grafana.asyncq.util.isTerminalJobStatus r`status; .grafana.asyncq.JOBS::update status:enlist "cancelled", message:enlist "cancelled by client", request:(::), finished:.z.p from .grafana.asyncq.JOBS where i=first rows];
    .grafana.asyncq.util.statusFromRow first select from .grafana.asyncq.JOBS where i=first rows
  };

/ Register the current IPC handle as a stream callback.
/ params:  req - dictionary sent by the Grafana backend.
/ returns: stream status dictionary.
.grafana.asyncq.stream.start:{[req]
    if[99h<>type req; '"stream start request must be a dictionary"];
    streamId:.grafana.asyncq.util.normalizeStreamId .grafana.asyncq.util.get[req;`StreamID; .grafana.asyncq.util.get[req;`RequestID;""]];
    limit:.grafana.asyncq.util.streamLimit[];
    rows:.grafana.asyncq.util.byStreamId streamId;
    .grafana.asyncq.STREAMS::delete from .grafana.asyncq.STREAMS where i in rows;
    if[limit<=count .grafana.asyncq.STREAMS; '"stream capacity exhausted: active streams fill MAX_ACTIVE_STREAMS"];
    .grafana.asyncq.STREAMS::.grafana.asyncq.STREAMS,enlist `streamId`handle`request`seq`started!(streamId;.z.w;req;0j;.z.p);
    `StreamID`Status`Progress`Error`Message`ErrorClass`StackTrace`Worker!(streamId;"running";0f;"";"";"";"";.grafana.asyncq.util.worker[])
  };

/ Stop a stream best-effort.
/ params:  streamId - char vector stream ID.
/ returns: stream status dictionary.
.grafana.asyncq.stream.stop:{[streamId]
    streamId:.grafana.asyncq.util.normalizeStreamId streamId;
    rows:.grafana.asyncq.util.byStreamId streamId;
    .grafana.asyncq.STREAMS::delete from .grafana.asyncq.STREAMS where i in rows;
    `StreamID`Status`Progress`Error`Message`ErrorClass`StackTrace`Worker!(streamId;"done";1f;"";"";"";"";.grafana.asyncq.util.worker[])
  };

/ Publish a table or grouped table to a Grafana stream.
/ params:  streamId - char vector stream ID
/          payload  - table or grouped table accepted by the Grafana parser
/ returns: stream status dictionary.
.grafana.asyncq.stream.publish:{[streamId;payload]
    streamId:.grafana.asyncq.util.normalizeStreamId streamId;
    rows:.grafana.asyncq.util.byStreamId streamId;
    r:.grafana.asyncq.util.requireSingleStreamRow[streamId;rows];
    idx:first rows;
    nextSeq:1+r`seq;
    .grafana.asyncq.STREAMS::update seq:nextSeq from .grafana.asyncq.STREAMS where i=idx;
    neg[r`handle] .grafana.asyncq.util.streamDict[streamId; "data"; nextSeq; payload; ""];
    neg[r`handle][];
    `StreamID`Status`Progress`Error!(streamId;"running";0f;"")
  };

/ Send a terminal error to a Grafana stream and remove the stream.
.grafana.asyncq.stream.error:{[streamId;err]
    streamId:.grafana.asyncq.util.normalizeStreamId streamId;
    rows:.grafana.asyncq.util.byStreamId streamId;
    if[0=count rows; :`StreamID`Status`Progress`Error`Message`ErrorClass`StackTrace`Worker!(streamId;"missing";0f;"stream not found";"stream not found";"missing";"";.grafana.asyncq.util.worker[])];
    r:.grafana.asyncq.util.requireSingleStreamRow[streamId;rows];
    neg[r`handle] .grafana.asyncq.util.streamDict[streamId; "error"; r`seq; (::); err];
    neg[r`handle][];
    .grafana.asyncq.stream.stop streamId
  };

/ Send a terminal done marker to Grafana and remove the stream.
.grafana.asyncq.stream.done:{[streamId]
    streamId:.grafana.asyncq.util.normalizeStreamId streamId;
    rows:.grafana.asyncq.util.byStreamId streamId;
    if[0=count rows; :`StreamID`Status`Progress`Error`Message`ErrorClass`StackTrace`Worker!(streamId;"missing";0f;"stream not found";"stream not found";"missing";"";.grafana.asyncq.util.worker[])];
    r:.grafana.asyncq.util.requireSingleStreamRow[streamId;rows];
    neg[r`handle] .grafana.asyncq.util.streamDict[streamId; "done"; r`seq; (::); ""];
    neg[r`handle][];
    .grafana.asyncq.stream.stop streamId
  };
