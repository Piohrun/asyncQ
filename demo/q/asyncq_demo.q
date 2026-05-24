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
.demo.asyncq.MAXROWS:5000;
.demo.asyncq.JOBDELAY:0D00:00:03.000000000;
.demo.asyncq.JOBS:([] jobId:(); status:(); progress:`float$(); query:(); request:(); result:(); error:(); submitted:`timestamp$(); due:`timestamp$(); finished:`timestamp$());

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

.demo.asyncq.statusDict:{[jobId;status;progress;err]
    `JobID`Status`Progress`Error!(.demo.asyncq.text jobId;.demo.asyncq.text status;progress;.demo.asyncq.text err)
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

.demo.asyncq.panoWrap:{[result;start;end;intervalMs]
    ([] timeWindowStart:enlist start; timeWindowEnd:enlist end; intervalMs:enlist intervalMs; resultType:enlist type result; rowCount:enlist count result)
  };

.demo.asyncq.panopticonRequest:{[req]
    qd:req`Query;
    p:req`Panopticon;
    ([] timeWindowStart:enlist p`TimeWindowStart; timeWindowEnd:enlist p`TimeWindowEnd; refId:enlist qd`RefID; originalQuery:enlist qd`OriginalQuery; compiledQuery:enlist qd`CompiledQuery)
  };

.demo.asyncq.counts:{
    ([] time:enlist .z.p; rows:enlist count .demo.asyncq.trade; streams:enlist count .grafana.asyncq.STREAMS; jobs:enlist count .demo.asyncq.JOBS)
  };

.demo.asyncq.submit:{[req]
    jobId:.demo.asyncq.get[req;`RequestID;string .z.p];
    query:req[`Query;`Query];
    now:.z.p;
    rows:.demo.asyncq.byJobId jobId;
    .demo.asyncq.JOBS::delete from .demo.asyncq.JOBS where i in rows;
    .demo.asyncq.JOBS::.demo.asyncq.JOBS,enlist `jobId`status`progress`query`request`result`error`submitted`due`finished!(enlist jobId;enlist "queued";0f;enlist query;enlist req;(::);enlist "";now;now+.demo.asyncq.JOBDELAY;0Np);
    .demo.asyncq.statusDict[jobId;"queued";0f;""]
  };

.demo.asyncq.status:{[jobId]
    rows:.demo.asyncq.byJobId jobId;
    if[0=count rows; '"job not found"];
    row:first select from .demo.asyncq.JOBS where i=first rows;
    status:.demo.asyncq.text row`status;
    progress:$[status in ("queued";"running");0.5;row`progress];
    .demo.asyncq.statusDict[row`jobId;status;progress;row`error]
  };

.demo.asyncq.result:{[jobId]
    rows:.demo.asyncq.byJobId jobId;
    if[0=count rows; '"job not found"];
    row:first select from .demo.asyncq.JOBS where i=first rows;
    if[not (.demo.asyncq.text row`status)~"done"; '"job not done"];
    row`result
  };

.demo.asyncq.cancel:{[jobId]
    rows:.demo.asyncq.byJobId jobId;
    if[0=count rows; :.demo.asyncq.statusDict[jobId;"missing";0f;"job not found"]];
    .demo.asyncq.JOBS::update status:enlist "cancelled", progress:1f, finished:.z.p from .demo.asyncq.JOBS where i=first rows;
    .demo.asyncq.status jobId
  };

.demo.asyncq.completeJob:{[idx]
    row:first select from .demo.asyncq.JOBS where i=idx;
    req:row`request;
    trapped:@[{(1b; .grafana.asyncq.util.evalQuery x)}; req; {(0b; x)}];
    ok:first trapped;
    payload:last trapped;
    $[ok;
        .demo.asyncq.JOBS::update status:enlist "done", progress:1f, result:enlist payload, error:enlist "", finished:.z.p from .demo.asyncq.JOBS where i=idx;
        .demo.asyncq.JOBS::update status:enlist "error", progress:1f, result:enlist (::), error:enlist payload, finished:.z.p from .demo.asyncq.JOBS where i=idx
      ];
  };

.demo.asyncq.completeDue:{
    if[0=count .demo.asyncq.JOBS; :()];
    pending:{.demo.asyncq.text[x] in ("queued";"running")} each .demo.asyncq.JOBS`status;
    due:(.demo.asyncq.JOBS`due)<=.z.p;
    .demo.asyncq.completeJob each where pending & due;
    ::
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
-1 "Try Panopticon-style dict: .demo.asyncq.panopticonSummary[]";
-1 "Try Panopticon request function: {[req] .demo.asyncq.panopticonRequest req}";
