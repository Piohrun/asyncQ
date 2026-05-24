/
AsyncQ Grafana helper protocol.

Load this file in a kdb+ process or, preferably, in a gateway that fronts worker
processes. The reference async implementation evaluates jobs in-process and is
therefore a protocol baseline, not a production worker-pool scheduler.

Streaming is push-oriented: `.grafana.asyncq.stream.start` stores the Grafana
IPC handle for a stream ID, and q code can call `.grafana.asyncq.stream.publish`
whenever new rows are ready.
\

.grafana.asyncq.JOBS:([] jobId:(); status:(); progress:`float$(); result:(); error:(); request:(); started:`timestamp$(); finished:`timestamp$());
.grafana.asyncq.STREAMS:([] streamId:(); handle:`int$(); request:(); seq:`long$(); started:`timestamp$());

.grafana.asyncq.util.get:{[d;k;default]
    $[k in key d; d k; default]
  };

.grafana.asyncq.util.text:{[cell]
    $[0=type cell; $[0=count cell; ""; .grafana.asyncq.util.text first cell]; cell]
  };

.grafana.asyncq.util.matchText:{[cell;target]
    (.grafana.asyncq.util.text cell)~.grafana.asyncq.util.text target
  };

.grafana.asyncq.util.byJobId:{[jobId]
    where .grafana.asyncq.util.matchText[;jobId] each .grafana.asyncq.JOBS`jobId
  };

.grafana.asyncq.util.byStreamId:{[streamId]
    where .grafana.asyncq.util.matchText[;streamId] each .grafana.asyncq.STREAMS`streamId
  };

.grafana.asyncq.util.statusDict:{[id;status;progress;err]
    `JobID`Status`Progress`Error!(.grafana.asyncq.util.text id;.grafana.asyncq.util.text status;progress;.grafana.asyncq.util.text err)
  };

.grafana.asyncq.util.streamDict:{[streamId;status;seq;payload;err]
    `MessageType`StreamID`Seq`Payload`Error!(status;streamId;seq;payload;err)
  };

.grafana.asyncq.util.evalQuery:{[req]
    req:$[98h=type req; first req; req];
    qd:req`Query;
    fn:.grafana.asyncq.util.get[qd;`PanopticonRequestFunction;""];
    $[0<count fn; (value fn) req; value qd`Query]
  };

/ Submit an async query.
/ params:  req - dictionary sent by the Grafana backend; Query text or Panopticon request function is evaluated.
/ returns: status dictionary with JobID, Status, Progress, Error.
.grafana.asyncq.async.submit:{[req]
    jobId:.grafana.asyncq.util.get[req;`RequestID; string .z.p];
    .grafana.asyncq.JOBS::.grafana.asyncq.JOBS,enlist `jobId`status`progress`result`error`request`started`finished!(enlist jobId;enlist "running";0f;(::);enlist "";req;.z.p;0Np);

    trapped:@[{(1b; .grafana.asyncq.util.evalQuery x)}; req; {(0b; x)}];
    ok:first trapped;
    payload:last trapped;
    rows:.grafana.asyncq.util.byJobId jobId;

    $[ok;
        .grafana.asyncq.JOBS::update status:enlist "done", progress:1f, result:enlist payload, error:enlist "", finished:.z.p from .grafana.asyncq.JOBS where i in rows;
        .grafana.asyncq.JOBS::update status:enlist "error", progress:1f, result:enlist (::), error:enlist payload, finished:.z.p from .grafana.asyncq.JOBS where i in rows
      ];
    .grafana.asyncq.util.statusDict[jobId; "queued"; 0f; ""]
  };

/ Return async query status.
/ params:  jobId - char vector job ID returned by async.submit.
/ returns: status dictionary.
.grafana.asyncq.async.status:{[jobId]
    rows:.grafana.asyncq.util.byJobId jobId;
    if[0=count rows; '"job not found"];
    r:first select from .grafana.asyncq.JOBS where i=first rows;
    .grafana.asyncq.util.statusDict[r`jobId; r`status; r`progress; r`error]
  };

/ Return async query result.
/ params:  jobId - char vector job ID returned by async.submit.
/ returns: table or grouped table.
.grafana.asyncq.async.result:{[jobId]
    rows:.grafana.asyncq.util.byJobId jobId;
    if[0=count rows; '"job not found"];
    r:first select from .grafana.asyncq.JOBS where i=first rows;
    if[not r[`status]~"done"; '"job not done"];
    r`result
  };

/ Cancel an async query best-effort.
/ params:  jobId - char vector job ID returned by async.submit.
/ returns: status dictionary.
.grafana.asyncq.async.cancel:{[jobId]
    rows:.grafana.asyncq.util.byJobId jobId;
    if[0=count rows; :.grafana.asyncq.util.statusDict[jobId; "missing"; 0f; "job not found"]];
    r:first select from .grafana.asyncq.JOBS where i=first rows;
    if[not r[`status] in ("done";"error"); .grafana.asyncq.JOBS::update status:enlist "cancelled", finished:.z.p from .grafana.asyncq.JOBS where i=first rows];
    .grafana.asyncq.async.status jobId
  };

/ Register the current IPC handle as a stream callback.
/ params:  req - dictionary sent by the Grafana backend.
/ returns: stream status dictionary.
.grafana.asyncq.stream.start:{[req]
    streamId:.grafana.asyncq.util.get[req;`StreamID; .grafana.asyncq.util.get[req;`RequestID; string .z.p]];
    rows:.grafana.asyncq.util.byStreamId streamId;
    .grafana.asyncq.STREAMS::delete from .grafana.asyncq.STREAMS where i in rows;
    .grafana.asyncq.STREAMS::.grafana.asyncq.STREAMS,enlist `streamId`handle`request`seq`started!(streamId;.z.w;req;0j;.z.p);
    `StreamID`Status`Progress`Error!(streamId;"running";0f;"")
  };

/ Stop a stream best-effort.
/ params:  streamId - char vector stream ID.
/ returns: stream status dictionary.
.grafana.asyncq.stream.stop:{[streamId]
    rows:.grafana.asyncq.util.byStreamId streamId;
    .grafana.asyncq.STREAMS::delete from .grafana.asyncq.STREAMS where i in rows;
    `StreamID`Status`Progress`Error!(streamId;"done";1f;"")
  };

/ Publish a table or grouped table to a Grafana stream.
/ params:  streamId - char vector stream ID
/          payload  - table or grouped table accepted by the Grafana parser
/ returns: stream status dictionary.
.grafana.asyncq.stream.publish:{[streamId;payload]
    rows:.grafana.asyncq.util.byStreamId streamId;
    if[0=count rows; '"stream not found"];
    idx:first rows;
    r:first select from .grafana.asyncq.STREAMS where i=idx;
    nextSeq:1+r`seq;
    .grafana.asyncq.STREAMS::update seq:nextSeq from .grafana.asyncq.STREAMS where i=idx;
    neg[r`handle] .grafana.asyncq.util.streamDict[streamId; "data"; nextSeq; payload; ""];
    neg[r`handle][];
    `StreamID`Status`Progress`Error!(streamId;"running";0f;"")
  };

/ Send a terminal error to a Grafana stream and remove the stream.
.grafana.asyncq.stream.error:{[streamId;err]
    rows:.grafana.asyncq.util.byStreamId streamId;
    if[0=count rows; :`StreamID`Status`Progress`Error!(streamId;"missing";0f;"stream not found")];
    r:first select from .grafana.asyncq.STREAMS where i=first rows;
    neg[r`handle] .grafana.asyncq.util.streamDict[streamId; "error"; r`seq; (::); err];
    neg[r`handle][];
    .grafana.asyncq.stream.stop streamId
  };

/ Send a terminal done marker to Grafana and remove the stream.
.grafana.asyncq.stream.done:{[streamId]
    rows:.grafana.asyncq.util.byStreamId streamId;
    if[0=count rows; :`StreamID`Status`Progress`Error!(streamId;"missing";0f;"stream not found")];
    r:first select from .grafana.asyncq.STREAMS where i=first rows;
    neg[r`handle] .grafana.asyncq.util.streamDict[streamId; "done"; r`seq; (::); ""];
    neg[r`handle][];
    .grafana.asyncq.stream.stop streamId
  };
