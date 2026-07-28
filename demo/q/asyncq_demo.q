/
AsyncQ demo kdb+ process.

Run from the repository root with:

  q demo/q/asyncq_demo.q -p 5000

This process loads the AsyncQ helper protocol, seeds a small in-memory trade
table, publishes new rows every second, and provides timer-backed async jobs for
Grafana Live demos.
\

\l q/asyncq_grafana.q

.demo.asyncq.SYMS:`AAPL`MSFT`GOOG`AMZN`KX;
.demo.asyncq.REPORTSYMS:`$("SYM",/:string 100000+til 1000);
.demo.asyncq.REPORTBOOKS:`EQ`FUT`OPT`FX;
.demo.asyncq.REPORTVENUES:`XNYS`XNAS`BATS`ARCX;
.demo.asyncq.REPORTSIDES:`B`S;
.demo.asyncq.REPORTBASE:.z.p-0D01:00:00.000000000;
.demo.asyncq.MAXROWS:5000;
.demo.asyncq.JOBDELAY:0D00:00:03.000000000;
.demo.asyncq.JOB_RETENTION:0D01:00:00.000000000;
.demo.asyncq.MAX_RETAINED_JOBS:1000;
.demo.asyncq.MAX_COMPLETIONS_PER_TICK:16;
/ Mutable demo settings are constrained by conservative hard ceilings in validateJobConfig.
.demo.asyncq.LIVE_JOB_STATUSES:("queued";"running");
.demo.asyncq.TERMINAL_JOB_STATUSES:("done";"error";"cancelled");
.demo.asyncq.JOB_COLUMNS:`jobId`status`progress`query`request`result`error`message`errorClass`stackTrace`submitted`due`finished`worker`resultType;
.demo.asyncq.JOB_COLUMN_TYPES:0 0 9 0 0 0 0 0 0 0 12 12 12 0 0h;
.demo.asyncq.JOBS:([] jobId:(); status:(); progress:`float$(); query:(); request:(); result:(); error:(); message:(); errorClass:(); stackTrace:(); submitted:`timestamp$(); due:`timestamp$(); finished:`timestamp$(); worker:(); resultType:());

.demo.asyncq.text:{[cell]
    $[0=type cell; $[0=count cell; ""; .demo.asyncq.text first cell]; cell]
  };

.demo.asyncq.get:{[d;k;default]
    $[k in key d; d k; default]
  };

.demo.asyncq.matchText:{[target;cell]
    (.demo.asyncq.text cell)~.demo.asyncq.text target
  };

.demo.asyncq.byJobId:{[jobId]
    where .demo.asyncq.matchText[jobId;] each .demo.asyncq.JOBS`jobId
  };

.demo.asyncq.validateJobConfig:{
    if[not ("queued";"running")~.demo.asyncq.LIVE_JOB_STATUSES; '"demo LIVE_JOB_STATUSES invariant mismatch"];
    if[not ("done";"error";"cancelled")~.demo.asyncq.TERMINAL_JOB_STATUSES; '"demo TERMINAL_JOB_STATUSES invariant mismatch"];
    if[-16h<>type .demo.asyncq.JOBDELAY; '"demo JOBDELAY must be a non-negative timespan atom"];
    if[0D00:00:00.000000000>.demo.asyncq.JOBDELAY; '"demo JOBDELAY must be a non-negative timespan atom"];
    if[1D00:00:00.000000000<.demo.asyncq.JOBDELAY; '"demo JOBDELAY exceeds the one-day hard ceiling"];
    if[-16h<>type .demo.asyncq.JOB_RETENTION; '"demo JOB_RETENTION must be a non-negative timespan atom"];
    if[0D00:00:00.000000000>.demo.asyncq.JOB_RETENTION; '"demo JOB_RETENTION must be a non-negative timespan atom"];
    if[30D00:00:00.000000000<.demo.asyncq.JOB_RETENTION; '"demo JOB_RETENTION exceeds the 30-day hard ceiling"];
    if[not .grafana.asyncq.util.integerAtom .demo.asyncq.MAX_RETAINED_JOBS; '"demo MAX_RETAINED_JOBS must be a positive integer atom"];
    if[1>.demo.asyncq.MAX_RETAINED_JOBS; '"demo MAX_RETAINED_JOBS must be a positive integer atom"];
    if[10000<.demo.asyncq.MAX_RETAINED_JOBS; '"demo MAX_RETAINED_JOBS exceeds the 10000-job hard ceiling"];
    if[not .grafana.asyncq.util.integerAtom .demo.asyncq.MAX_COMPLETIONS_PER_TICK; '"demo MAX_COMPLETIONS_PER_TICK must be a positive integer atom"];
    if[1>.demo.asyncq.MAX_COMPLETIONS_PER_TICK; '"demo MAX_COMPLETIONS_PER_TICK must be a positive integer atom"];
    if[256<.demo.asyncq.MAX_COMPLETIONS_PER_TICK; '"demo MAX_COMPLETIONS_PER_TICK exceeds the 256-job hard ceiling"];
    (::)
  };

.demo.asyncq.validateJobSchema:{
    expectedColumns:`jobId`status`progress`query`request`result`error`message`errorClass`stackTrace`submitted`due`finished`worker`resultType;
    expectedTypes:0 0 9 0 0 0 0 0 0 0 12 12 12 0 0h;
    if[not expectedColumns~.demo.asyncq.JOB_COLUMNS; '"demo JOB_COLUMNS invariant mismatch"];
    if[not expectedTypes~.demo.asyncq.JOB_COLUMN_TYPES; '"demo JOB_COLUMN_TYPES invariant mismatch"];
    if[98h<>type .demo.asyncq.JOBS; '"demo JOBS must be an unkeyed table"];
    if[not .demo.asyncq.JOB_COLUMNS~cols .demo.asyncq.JOBS; '"demo JOBS schema mismatch"];
    if[10000<count .demo.asyncq.JOBS; '"demo JOBS exceeds the 10000-row hard ceiling"];
    columnTypes:type each value flip .demo.asyncq.JOBS;
    if[not .demo.asyncq.JOB_COLUMN_TYPES~columnTypes; '"demo JOBS column types mismatch"];
    (::)
  };

.demo.asyncq.allCharVectors:{[values]
    all 10h=type each values
  };

.demo.asyncq.validateJobRows:{
    jobs:.demo.asyncq.JOBS;
    if[0=count jobs; :(::)];
    ids:jobs`jobId;
    .grafana.asyncq.util.normalizeJobId each ids;
    duplicateRows:where (til count ids)<>ids?ids;
    if[count duplicateRows; '"duplicate retained job id: ",ids first duplicateRows];

    statuses:jobs`status;
    if[not .demo.asyncq.allCharVectors statuses; '"demo JOBS status values must be char vectors"];
    if[not all statuses in (.demo.asyncq.LIVE_JOB_STATUSES,.demo.asyncq.TERMINAL_JOB_STATUSES); '"demo JOBS contains an invalid status"];
    if[not .demo.asyncq.allCharVectors jobs`error; '"demo JOBS error values must be char vectors"];
    if[not .demo.asyncq.allCharVectors jobs`message; '"demo JOBS message values must be char vectors"];
    if[not .demo.asyncq.allCharVectors jobs`errorClass; '"demo JOBS errorClass values must be char vectors"];
    if[not .demo.asyncq.allCharVectors jobs`stackTrace; '"demo JOBS stackTrace values must be char vectors"];
    if[not .demo.asyncq.allCharVectors jobs`worker; '"demo JOBS worker values must be char vectors"];
    if[not .demo.asyncq.allCharVectors jobs`resultType; '"demo JOBS resultType values must be char vectors"];

    queryTypes:type each jobs`query;
    requestTypes:type each jobs`request;
    validQueries:(10h=queryTypes)|{(::)~x} each jobs`query;
    validRequests:(99h=requestTypes)|{(::)~x} each jobs`request;
    if[not all validQueries; '"demo JOBS query values must be char vectors or generic null"];
    if[not all validRequests; '"demo JOBS request values must be dictionaries or generic null"];
    if[not all 1=count each jobs`result; '"demo JOBS result cells must contain one wrapped payload"];

    terminalMask:statuses in .demo.asyncq.TERMINAL_JOB_STATUSES;
    liveMask:not terminalMask;
    if[any liveMask & 10h<>queryTypes; '"live demo jobs must retain a char-vector query"];
    if[any liveMask & 99h<>requestTypes; '"live demo jobs must retain a request dictionary"];
    if[any null jobs`submitted; '"demo JOBS submitted timestamps must not be null"];
    if[any null jobs`due; '"demo JOBS due timestamps must not be null"];
    if[any terminalMask & null jobs`finished; '"terminal demo jobs must have a finished timestamp"];
    if[any liveMask & not null jobs`finished; '"live demo jobs must not have a finished timestamp"];
    progressValues:jobs`progress;
    if[any null progressValues; '"demo JOBS progress values must not be null"];
    invalidProgress:(0f>progressValues)|(progressValues>1f);
    if[any invalidProgress; '"demo JOBS progress values must be between 0 and 1"];
    (::)
  };

.demo.asyncq.validateJobs:{
    .demo.asyncq.validateJobConfig[];
    .demo.asyncq.validateJobSchema[];
    .demo.asyncq.validateJobRows[];
    (::)
  };

.demo.asyncq.requireSingleJobRow:{[jobId;rows]
    if[1<count rows; '"duplicate retained job id: ",jobId];
    if[0=count rows; '"job not found"];
    first select from .demo.asyncq.JOBS where i=first rows
  };

.demo.asyncq.validateSubmitRequest:{[req]
    if[99h<>type req; '"demo async submit request must be a dictionary"];
    jobId:.grafana.asyncq.util.normalizeJobId .grafana.asyncq.util.get[req;`RequestID;""];
    queryDict:.grafana.asyncq.util.get[req;`Query;(::)];
    if[99h<>type queryDict; '"demo async request Query must be a dictionary"];
    query:.grafana.asyncq.util.get[queryDict;`Query;(::)];
    if[10h<>type query; '"demo async request Query.Query must be a char vector"];
    functionName:.grafana.asyncq.util.get[queryDict;`PanopticonRequestFunction;""];
    if[not ""~functionName; .grafana.asyncq.util.resolvePanopticonFunction functionName];
    `JobID`Query!(jobId;query)
  };

.demo.asyncq.cleanupJobs:{[reserve]
    if[not .grafana.asyncq.util.integerAtom reserve; '"demo job cleanup reserve must be a non-negative integer atom"];
    if[0>reserve; '"demo job cleanup reserve must be a non-negative integer atom"];
    .demo.asyncq.validateJobs[];
    limit:.demo.asyncq.MAX_RETAINED_JOBS;
    if[limit<reserve; '"demo job capacity is smaller than the requested reservation"];

    terminalMask:(.demo.asyncq.JOBS`status) in .demo.asyncq.TERMINAL_JOB_STATUSES;
    terminalRows:where terminalMask;
    if[count terminalRows;
      .demo.asyncq.JOBS::update query:(::), request:(::) from .demo.asyncq.JOBS where i in terminalRows];

    now:.z.p;
    cutoff:now-.demo.asyncq.JOB_RETENTION;
    if[(null cutoff)|cutoff>now; '"demo JOB_RETENTION overflows timestamp arithmetic"];
    expiredRows:where terminalMask & (.demo.asyncq.JOBS`finished)<cutoff;
    if[count expiredRows;
      .demo.asyncq.JOBS::delete from .demo.asyncq.JOBS where i in expiredRows];

    target:limit-reserve;
    excess:(count .demo.asyncq.JOBS)-target;
    if[0>=excess; :(::)];

    terminalMask:(.demo.asyncq.JOBS`status) in .demo.asyncq.TERMINAL_JOB_STATUSES;
    terminalRows:where terminalMask;
    orderedRows:terminalRows iasc (.demo.asyncq.JOBS`finished) terminalRows;
    evictCount:excess&count orderedRows;
    evictRows:evictCount#orderedRows;
    if[count evictRows;
      .demo.asyncq.JOBS::delete from .demo.asyncq.JOBS where i in evictRows];
    if[excess>evictCount; '"demo job capacity exhausted: retained live jobs fill MAX_RETAINED_JOBS"];
    (::)
  };

.demo.asyncq.statusDict:{[jobId;status;progress;err]
    .grafana.asyncq.util.statusDict `JobID`Status`Progress`Error`Message`ErrorClass`StackTrace`Worker`Started`Finished`ResultType!(jobId; status; progress; err; err; ""; ""; .grafana.asyncq.util.worker[]; 0Np; 0Np; "")
  };

.demo.asyncq.statusFromRow:{[row;status;progress]
    .grafana.asyncq.util.statusDict `JobID`Status`Progress`Error`Message`ErrorClass`StackTrace`Worker`Started`Finished`ResultType!(row`jobId; status; progress; row`error; row`message; row`errorClass; row`stackTrace; row`worker; row`submitted; row`finished; row`resultType)
  };

.demo.asyncq.currentStatusFromRow:{[row]
    status:.demo.asyncq.text row`status;
    progress:$[status in .demo.asyncq.LIVE_JOB_STATUSES;0.5;row`progress];
    .demo.asyncq.statusFromRow[row;status;progress]
  };

.demo.asyncq.seed:{[n]
    base:.z.p-0D00:05:00.000000000;
    ([] time:base+1000000000*til n; sym:n?.demo.asyncq.SYMS; price:100+0.01*n?10000; size:10*1+n?50)
  };

.demo.asyncq.nextRows:{[n]
    ([] time:.z.p+1000000*til n; sym:n?.demo.asyncq.SYMS; price:100+0.01*n?10000; size:10*1+n?100)
  };

.demo.asyncq.trim:{[t]
    $[.demo.asyncq.MAXROWS<count t; (neg .demo.asyncq.MAXROWS)#t; t]
  };

.demo.asyncq.trade:.demo.asyncq.seed 300;

.demo.asyncq.buildReportRows:{[n]
    idx:til n;
    ([] time:.demo.asyncq.REPORTBASE+1000000*idx; sym:.demo.asyncq.REPORTSYMS idx mod count .demo.asyncq.REPORTSYMS; book:.demo.asyncq.REPORTBOOKS idx mod count .demo.asyncq.REPORTBOOKS; price:50+0.01*idx mod 50000; size:100+10*idx mod 1000; volume:100000+10*idx; venue:.demo.asyncq.REPORTVENUES idx mod count .demo.asyncq.REPORTVENUES; side:.demo.asyncq.REPORTSIDES idx mod count .demo.asyncq.REPORTSIDES)
  };

.demo.asyncq.REPORTDATA:.demo.asyncq.buildReportRows 100000;

.demo.asyncq.reportRows:{[n]
    n:1000|`int$n;
    n:100000&n;
    n#.demo.asyncq.REPORTDATA
  };

.demo.asyncq.reportSummary:{[n]
    rows:.demo.asyncq.reportRows n;
    select lastTime:last time, lastPrice:last price, trades:count i, turnover:sum price*size, avgSize:avg size by sym from rows
  };

.demo.asyncq.latest:{[n]
    n#reverse .demo.asyncq.trade
  };

.demo.asyncq.streamTicks:{
    0#.demo.asyncq.trade
  };

.demo.asyncq.lastPrices:{
    select lastPrice:last price, trades:count i by sym from .demo.asyncq.trade where time>.z.p-0D00:05:00.000000000
  };

.demo.asyncq.slowAgg:{
    select avgPrice:avg price, maxPrice:max price, minPrice:min price, trades:count i, turnover:sum price*size by sym from .demo.asyncq.trade where time>.z.p-0D00:05:00.000000000
  };

.demo.asyncq.poolProbe:{[label;delayMs]
    started:.z.p;
    delaySeconds:1|ceiling delayMs%1000;
    system "sleep ",string delaySeconds;
    finished:.z.p;
    elapsedNs:`long$(finished-started);
    ([] probe:enlist .demo.asyncq.text label; handle:enlist .z.w; delayMs:enlist delayMs; started:enlist started; finished:enlist finished; elapsedMs:enlist elapsedNs div 1000000; tradeRows:enlist count .demo.asyncq.trade)
  };

.demo.asyncq.deferred:{[result]
    result
  };

.demo.asyncq.panopticonSummary:{
    lastAAPL:last exec price from .demo.asyncq.trade where sym=`AAPL;
    `sym`lastPrice`rows!(`AAPL;lastAAPL;count .demo.asyncq.trade)
  };

.demo.asyncq.panoScalar:{42};

.demo.asyncq.panoVector:{10 20 30 40 50};

.demo.asyncq.panoString:{"ready"};

.demo.asyncq.panoKeyed:{
    `sym xkey select lastPrice:last price, lastSize:last size by sym from .demo.asyncq.trade
  };

.demo.asyncq.panoRows:{
    aapl:last exec price from .demo.asyncq.trade where sym=`AAPL;
    msft:last exec price from .demo.asyncq.trade where sym=`MSFT;
    (`sym`metric`value!(`AAPL;"lastPrice";aapl);`sym`metric`value!(`MSFT;"lastPrice";msft);`sym`metric`value!(`ALL;"rows";"f"$count .demo.asyncq.trade))
  };

.demo.asyncq.panoSparseRows:{
    aapl:last exec price from .demo.asyncq.trade where sym=`AAPL;
    (`sym`price!(`AAPL;aapl);`venue`sym!("XNYS";`MSFT);`price`sym!(101f;`GOOG))
  };

.demo.asyncq.panoMixedNumeric:{
    (`sym`value!(`AAPL;2);`sym`value!(`MSFT;2.5))
  };

.demo.asyncq.panoWrap:{[result;start;end;intervalMs]
    ([] timeWindowStart:enlist start; timeWindowEnd:enlist end; intervalMs:enlist intervalMs; resultType:enlist type result; rowCount:enlist count result)
  };

.demo.asyncq.panopticonRequest:{[req]
    qd:req`Query;
    p:req`Panopticon;
    ([] timeWindowStart:enlist p`TimeWindowStart; topLevelStart:enlist req`TimeWindowStart; focusTime:enlist req`FocusTime; intervalMs:enlist req`IntervalMs; refId:enlist qd`RefID; originalQuery:enlist qd`OriginalQuery; compiledQuery:enlist qd`CompiledQuery)
  };

.grafana.asyncq.TRUSTED_PANOPTICON_FUNCTIONS:distinct .grafana.asyncq.TRUSTED_PANOPTICON_FUNCTIONS,`.demo.asyncq.panopticonRequest;

.demo.asyncq.compatMatrixDirect:{
    ([] feature:enlist "plain q expression/function call"; verdict:enlist "Direct"; mode:enlist "sync or pluginAsync"; rows:enlist count .demo.asyncq.trade; observedAt:enlist .z.p)
  };

.demo.asyncq.panoUnsupportedNumericKeys:{
    1 2!3 4
  };

.demo.asyncq.panoUnsupportedAdapted:{
    ([] dictKey:string 1 2; dictValue:3 4; verdict:2#enlist "Direct after table adapter")
  };

.demo.asyncq.counts:{
    ([] time:enlist .z.p; rows:enlist count .demo.asyncq.trade; streams:enlist count .grafana.asyncq.STREAMS; jobs:enlist count .demo.asyncq.JOBS)
  };

.demo.asyncq.submit:{[req]
    validated:.demo.asyncq.validateSubmitRequest req;
    jobId:validated`JobID;
    query:validated`Query;
    .demo.asyncq.cleanupJobs 0;
    rows:.demo.asyncq.byJobId jobId;
    if[1<count rows; '"duplicate retained job id: ",jobId];
    if[1=count rows;
      row:.demo.asyncq.requireSingleJobRow[jobId;rows];
      :.demo.asyncq.statusFromRow[row;.demo.asyncq.text row`status;row`progress]];
    .demo.asyncq.cleanupJobs 1;
    now:.z.p;
    due:now+.demo.asyncq.JOBDELAY;
    if[(null due)|due<now; '"demo JOBDELAY overflows timestamp arithmetic"];
    worker:.grafana.asyncq.util.worker[];
    .demo.asyncq.JOBS::.demo.asyncq.JOBS,enlist `jobId`status`progress`query`request`result`error`message`errorClass`stackTrace`submitted`due`finished`worker`resultType!(jobId;"queued";0f;query;req;enlist (::);"";"";"";"";now;due;0Np;worker;"");
    .grafana.asyncq.util.statusDict `JobID`Status`Progress`Error`Message`ErrorClass`StackTrace`Worker`Started`Finished`ResultType!(jobId; "queued"; 0f; ""; ""; ""; ""; worker; now; 0Np; "")
  };

.demo.asyncq.status:{[jobId]
    jobId:.grafana.asyncq.util.normalizeJobId jobId;
    .demo.asyncq.cleanupJobs 0;
    rows:.demo.asyncq.byJobId jobId;
    row:.demo.asyncq.requireSingleJobRow[jobId;rows];
    .demo.asyncq.currentStatusFromRow row
  };

.demo.asyncq.result:{[jobId]
    jobId:.grafana.asyncq.util.normalizeJobId jobId;
    .demo.asyncq.cleanupJobs 0;
    rows:.demo.asyncq.byJobId jobId;
    row:.demo.asyncq.requireSingleJobRow[jobId;rows];
    if[not (.demo.asyncq.text row`status)~"done"; '"job not done"];
    first row`result
  };

.demo.asyncq.cancel:{[jobId]
    jobId:.grafana.asyncq.util.normalizeJobId jobId;
    .demo.asyncq.cleanupJobs 0;
    rows:.demo.asyncq.byJobId jobId;
    if[0=count rows; :.demo.asyncq.statusDict[jobId;"missing";0f;"job not found"]];
    row:.demo.asyncq.requireSingleJobRow[jobId;rows];
    if[not (.demo.asyncq.text row`status) in .demo.asyncq.TERMINAL_JOB_STATUSES;
      .demo.asyncq.JOBS::update status:enlist "cancelled", progress:1f, query:(::), request:(::), result:enlist enlist (::), message:enlist "cancelled by client", finished:.z.p from .demo.asyncq.JOBS where i=first rows;
      row:first select from .demo.asyncq.JOBS where i=first rows];
    .demo.asyncq.currentStatusFromRow row
  };

.demo.legacy.submit:{[req]
    s:.demo.asyncq.submit req;
    `id`state`pct`note`err!(s`JobID;s`Status;s`Progress;s`Message;s`Error)
  };

.demo.legacy.status:{[jobId]
    s:.demo.asyncq.status jobId;
    `id`state`pct`note`err!(s`JobID;s`Status;s`Progress;s`Message;s`Error)
  };

.demo.legacy.result:{[jobId]
    payload:.demo.asyncq.result jobId;
    `payload`kind!(payload;.grafana.asyncq.util.describe payload)
  };

.demo.legacy.cancel:{[jobId]
    s:.demo.asyncq.cancel jobId;
    `id`state`pct`note`err!(s`JobID;s`Status;s`Progress;s`Message;s`Error)
  };

.demo.asyncq.completeJob:{[idx]
    if[not .grafana.asyncq.util.integerAtom idx; '"demo completion row index must be a non-negative integer atom"];
    if[0>idx; '"demo completion row index must be a non-negative integer atom"];
    if[count[.demo.asyncq.JOBS]<=idx; '"demo completion row index is out of range"];
    row:first select from .demo.asyncq.JOBS where i=idx;
    jobId:.grafana.asyncq.util.normalizeJobId row`jobId;
    rows:.demo.asyncq.byJobId jobId;
    .demo.asyncq.requireSingleJobRow[jobId;rows];
    if[not (.demo.asyncq.text row`status) in .demo.asyncq.LIVE_JOB_STATUSES; '"demo job is not pending completion"];
    req:row`request;
    trapped:.grafana.asyncq.util.trapEval req;
    ok:first trapped;
    payload:last trapped;
    $[ok;
        .demo.asyncq.JOBS::update status:enlist "done", progress:1f, query:(::), request:(::), result:enlist enlist payload, error:enlist "", message:enlist "", errorClass:enlist "", stackTrace:enlist "", finished:.z.p, resultType:enlist .grafana.asyncq.util.describe payload from .demo.asyncq.JOBS where i=idx;
        .demo.asyncq.JOBS::update status:enlist "error", progress:1f, query:(::), request:(::), result:enlist enlist (::), error:enlist .grafana.asyncq.util.text payload`Error, message:enlist .grafana.asyncq.util.text payload`Message, errorClass:enlist .grafana.asyncq.util.text payload`ErrorClass, stackTrace:enlist .grafana.asyncq.util.text payload`StackTrace, finished:.z.p, resultType:enlist "" from .demo.asyncq.JOBS where i=idx
      ];
    (::)
  };

.demo.asyncq.completeDue:{
    .demo.asyncq.cleanupJobs 0;
    if[0=count .demo.asyncq.JOBS; :(::)];
    pending:{.demo.asyncq.text[x] in .demo.asyncq.LIVE_JOB_STATUSES} each .demo.asyncq.JOBS`status;
    due:(.demo.asyncq.JOBS`due)<=.z.p;
    completionLimit:.demo.asyncq.MAX_COMPLETIONS_PER_TICK&.demo.asyncq.MAX_RETAINED_JOBS;
    dueRows:where pending & due;
    completionRows:(completionLimit&count dueRows)#dueRows;
    .demo.asyncq.completeJob each completionRows;
    (::)
  };

.demo.asyncq.streamIds:{[ids]
    $[10=type ids; enlist ids; .demo.asyncq.text each ids]
  };

.demo.asyncq.publish:{[rows]
    ids:.demo.asyncq.streamIds .grafana.asyncq.STREAMS`streamId;
    if[0=count ids; :()];
    {.[.grafana.asyncq.stream.publish;(x;y);{[err] (::)}]}[;rows] each ids;
    ::
  };

.demo.asyncq.tick:{
    rows:.demo.asyncq.nextRows 5;
    .demo.asyncq.trade::.demo.asyncq.trim .demo.asyncq.trade,rows;
    .demo.asyncq.publish rows;
    .demo.asyncq.completeDue[];
  };

.grafana.asyncq.async.submit:.demo.asyncq.submit;
.grafana.asyncq.async.status:.demo.asyncq.status;
.grafana.asyncq.async.result:.demo.asyncq.result;
.grafana.asyncq.async.cancel:.demo.asyncq.cancel;

.z.pc:{[h]
    .grafana.asyncq.STREAMS::delete from .grafana.asyncq.STREAMS where handle=h;
  };

.z.ts:{.demo.asyncq.tick[]};
\t 1000

-1 "AsyncQ demo q process ready on port ",string system "p";
-1 "Try sync:  .demo.asyncq.latest 10";
-1 "Try async: .demo.asyncq.slowAgg[]";
-1 "Try sync pool probe: .demo.asyncq.poolProbe[\"A\";3000]";
-1 "Try Panopticon-style dict: .demo.asyncq.panopticonSummary[]";
-1 "Try Panopticon request function: .demo.asyncq.panopticonRequest";
-1 "Try compatibility matrix direct fixture: .demo.asyncq.compatMatrixDirect[]";
-1 "Try legacy async adapter functions: .demo.legacy.submit/status/result/cancel";
-1 "Try large report rows: .demo.asyncq.reportRows 10000";
