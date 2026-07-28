\l demo/q/asyncq_demo.q
\t 0

/ Isolated regression tests for the timer-backed demo async scheduler.
/ Run from the repository root under bounded q flags; no IPC port is opened.

.demoasynctest.EMPTY_JOBS:0#.demo.asyncq.JOBS;
.demoasynctest.ORIGINAL_JOB_DELAY:.demo.asyncq.JOBDELAY;
.demoasynctest.ORIGINAL_JOB_RETENTION:.demo.asyncq.JOB_RETENTION;
.demoasynctest.ORIGINAL_MAX_RETAINED_JOBS:.demo.asyncq.MAX_RETAINED_JOBS;
.demoasynctest.ORIGINAL_MAX_COMPLETIONS_PER_TICK:.demo.asyncq.MAX_COMPLETIONS_PER_TICK;
.demoasynctest.ORIGINAL_LIVE_JOB_STATUSES:.demo.asyncq.LIVE_JOB_STATUSES;
.demoasynctest.ORIGINAL_TERMINAL_JOB_STATUSES:.demo.asyncq.TERMINAL_JOB_STATUSES;
.demoasynctest.ORIGINAL_JOB_COLUMNS:.demo.asyncq.JOB_COLUMNS;
.demoasynctest.ORIGINAL_JOB_COLUMN_TYPES:.demo.asyncq.JOB_COLUMN_TYPES;
.demoasynctest.failures:();

.demoasynctest.restore:{
    .demo.asyncq.JOBS::.demoasynctest.EMPTY_JOBS;
    .demo.asyncq.JOBDELAY::.demoasynctest.ORIGINAL_JOB_DELAY;
    .demo.asyncq.JOB_RETENTION::.demoasynctest.ORIGINAL_JOB_RETENTION;
    .demo.asyncq.MAX_RETAINED_JOBS::.demoasynctest.ORIGINAL_MAX_RETAINED_JOBS;
    .demo.asyncq.MAX_COMPLETIONS_PER_TICK::.demoasynctest.ORIGINAL_MAX_COMPLETIONS_PER_TICK;
    .demo.asyncq.LIVE_JOB_STATUSES::.demoasynctest.ORIGINAL_LIVE_JOB_STATUSES;
    .demo.asyncq.TERMINAL_JOB_STATUSES::.demoasynctest.ORIGINAL_TERMINAL_JOB_STATUSES;
    .demo.asyncq.JOB_COLUMNS::.demoasynctest.ORIGINAL_JOB_COLUMNS;
    .demo.asyncq.JOB_COLUMN_TYPES::.demoasynctest.ORIGINAL_JOB_COLUMN_TYPES;
    (::)
  };

.demoasynctest.reset:{
    .demoasynctest.restore[];
    (::)
  };

.demoasynctest.assert:{[name;condition]
    if[not 1b~condition; .demoasynctest.failures,:enlist name];
    (::)
  };

.demoasynctest.assertMatch:{[name;expected;actual]
    .demoasynctest.assert[name;expected~actual]
  };

.demoasynctest.request:{[jobId;query]
    `RequestID`Query!(
      jobId;
      `Query`PanopticonRequestFunction!(query;""))
  };

.demoasynctest.row:{[jobId]
    rows:.demo.asyncq.byJobId jobId;
    .demo.asyncq.requireSingleJobRow[jobId;rows]
  };

.demoasynctest.markExpired:{[jobId]
    rows:.demo.asyncq.byJobId jobId;
    .demo.asyncq.JOBS::update finished:.z.p-0D00:02:00.000000000 from .demo.asyncq.JOBS where i in rows;
    (::)
  };

.demoasynctest.assertPayloadCleared:{[label;jobId]
    row:.demoasynctest.row jobId;
    .demoasynctest.assertMatch[(label," query cleared");(::);row`query];
    .demoasynctest.assertMatch[(label," request cleared");(::);row`request];
    (::)
  };

.demoasynctest.call0:{[fn]
    fn[]
  };

.demoasynctest.testTerminalTransitions:{
    .demoasynctest.reset[];
    .demo.asyncq.JOBDELAY::0D00:00:00.000000000;

    successSubmit:.demo.asyncq.submit .demoasynctest.request["success-job";"40+2"];
    .demoasynctest.assertMatch["success submit is queued";"queued";successSubmit`Status];
    .demo.asyncq.completeDue[];
    .demoasynctest.assertMatch["success reaches done";"done";(.demo.asyncq.status "success-job")`Status];
    .demoasynctest.assertMatch["success result retained";42;.demo.asyncq.result "success-job"];
    .demoasynctest.assertPayloadCleared["success terminal";"success-job"];

    .demo.asyncq.submit .demoasynctest.request["error-job";"1+`bad"];
    .demo.asyncq.completeDue[];
    errorStatus:.demo.asyncq.status "error-job";
    .demoasynctest.assertMatch["evaluation error reaches error";"error";errorStatus`Status];
    .demoasynctest.assert["evaluation error text retained";0<count errorStatus`Error];
    .demoasynctest.assertPayloadCleared["error terminal";"error-job"];

    .demo.asyncq.JOBDELAY::0D00:01:00.000000000;
    .demo.asyncq.submit .demoasynctest.request["cancel-job";"0+7"];
    cancelStatus:.demo.asyncq.cancel "cancel-job";
    .demoasynctest.assertMatch["cancel reaches cancelled";"cancelled";cancelStatus`Status];
    .demoasynctest.assertPayloadCleared["cancel terminal";"cancel-job"];

    doneBefore:.demoasynctest.row "success-job";
    .demo.asyncq.cancel "success-job";
    doneAfter:.demoasynctest.row "success-job";
    .demoasynctest.assertMatch["cancel does not overwrite done";doneBefore;doneAfter];

    errorBefore:.demoasynctest.row "error-job";
    .demo.asyncq.cancel "error-job";
    errorAfter:.demoasynctest.row "error-job";
    .demoasynctest.assertMatch["cancel does not overwrite error";errorBefore;errorAfter];

    cancelBefore:.demoasynctest.row "cancel-job";
    .demo.asyncq.cancel "cancel-job";
    cancelAfter:.demoasynctest.row "cancel-job";
    .demoasynctest.assertMatch["cancel does not overwrite cancelled";cancelBefore;cancelAfter];
    .demoasynctest.assertMatch["mixed terminal transitions preserve scheduler invariants";(::);.demo.asyncq.validateJobs[]];
    (::)
  };

.demoasynctest.testIdempotentSubmit:{
    .demoasynctest.reset[];
    .demo.asyncq.JOBDELAY::0D00:00:00.000000000;

    firstSubmit:.demo.asyncq.submit .demoasynctest.request["idempotent-job";"40+2"];
    before:.demoasynctest.row "idempotent-job";
    duplicateSubmit:.demo.asyncq.submit .demoasynctest.request["idempotent-job";"999"];
    after:.demoasynctest.row "idempotent-job";
    .demoasynctest.assertMatch["duplicate submit returns retained status";"queued";duplicateSubmit`Status];
    .demoasynctest.assertMatch["duplicate submit retains one row";1;count .demo.asyncq.JOBS];
    .demoasynctest.assertMatch["duplicate submit preserves queued row";before;after];
    .demoasynctest.assertMatch["duplicate submit preserves start time";firstSubmit`Started;duplicateSubmit`Started];

    .demo.asyncq.completeDue[];
    .demoasynctest.assertMatch["original duplicate query executes once";42;.demo.asyncq.result "idempotent-job"];
    terminalBefore:.demoasynctest.row "idempotent-job";
    terminalDuplicate:.demo.asyncq.submit .demoasynctest.request["idempotent-job";"1000"];
    terminalAfter:.demoasynctest.row "idempotent-job";
    .demoasynctest.assertMatch["terminal duplicate returns done";"done";terminalDuplicate`Status];
    .demoasynctest.assertMatch["terminal duplicate preserves row";terminalBefore;terminalAfter];
    .demoasynctest.assertMatch["terminal duplicate does not re-execute";42;.demo.asyncq.result "idempotent-job"];

    .demo.asyncq.submit .demoasynctest.request["idempotent-error";"1+`bad"];
    .demo.asyncq.completeDue[];
    errorBefore:.demoasynctest.row "idempotent-error";
    errorDuplicate:.demo.asyncq.submit .demoasynctest.request["idempotent-error";"0+99"];
    errorAfter:.demoasynctest.row "idempotent-error";
    .demoasynctest.assertMatch["error duplicate returns error";"error";errorDuplicate`Status];
    .demoasynctest.assertMatch["error duplicate preserves row";errorBefore;errorAfter];

    .demo.asyncq.JOBDELAY::0D00:01:00.000000000;
    .demo.asyncq.submit .demoasynctest.request["idempotent-cancel";"0+7"];
    .demo.asyncq.cancel "idempotent-cancel";
    cancelBefore:.demoasynctest.row "idempotent-cancel";
    cancelDuplicate:.demo.asyncq.submit .demoasynctest.request["idempotent-cancel";"0+88"];
    cancelAfter:.demoasynctest.row "idempotent-cancel";
    .demoasynctest.assertMatch["cancel duplicate returns cancelled";"cancelled";cancelDuplicate`Status];
    .demoasynctest.assertMatch["cancel duplicate preserves row";cancelBefore;cancelAfter];
    (::)
  };

.demoasynctest.testResultRoundTrips:{
    .demoasynctest.reset[];
    .demo.asyncq.JOBDELAY::0D00:00:00.000000000;
    expectedDict:`a`b!1 2;
    expectedTable:([] a:1 2;b:3 4);

    .demo.asyncq.submit .demoasynctest.request["scalar-result";"42"];
    .demo.asyncq.submit .demoasynctest.request["vector-result";"10 20 30"];
    .demo.asyncq.submit .demoasynctest.request["dict-result";"`a`b!1 2"];
    .demo.asyncq.submit .demoasynctest.request["table-result";"([] a:1 2;b:3 4)"];
    .demo.asyncq.completeDue[];

    .demoasynctest.assertMatch["scalar result round trips";42;.demo.asyncq.result "scalar-result"];
    .demoasynctest.assertMatch["vector result round trips";10 20 30;.demo.asyncq.result "vector-result"];
    .demoasynctest.assertMatch["dictionary result round trips";expectedDict;.demo.asyncq.result "dict-result"];
    .demoasynctest.assertMatch["table result round trips";expectedTable;.demo.asyncq.result "table-result"];
    .demoasynctest.assertPayloadCleared["scalar result";"scalar-result"];
    .demoasynctest.assertPayloadCleared["vector result";"vector-result"];
    .demoasynctest.assertPayloadCleared["dictionary result";"dict-result"];
    .demoasynctest.assertPayloadCleared["table result";"table-result"];
    .demoasynctest.assertMatch["result shapes preserve scheduler invariants";(::);.demo.asyncq.validateJobs[]];
    .demoasynctest.assertMatch["result shapes preserve column types";.demo.asyncq.JOB_COLUMN_TYPES;type each value flip .demo.asyncq.JOBS];
    (::)
  };

.demoasynctest.testPublicCleanup:{
    .demoasynctest.reset[];
    .demo.asyncq.JOB_RETENTION::0D00:01:00.000000000;
    .demo.asyncq.JOBDELAY::0D00:00:00.000000000;
    .demo.asyncq.submit .demoasynctest.request["expired-status";"0+1"];
    .demo.asyncq.submit .demoasynctest.request["target-status";"0+2"];
    .demo.asyncq.completeDue[];
    .demoasynctest.markExpired "expired-status";
    .demo.asyncq.status "target-status";
    .demoasynctest.assertMatch["status cleans expired jobs";0;count .demo.asyncq.byJobId "expired-status"];

    .demoasynctest.reset[];
    .demo.asyncq.JOB_RETENTION::0D00:01:00.000000000;
    .demo.asyncq.JOBDELAY::0D00:00:00.000000000;
    .demo.asyncq.submit .demoasynctest.request["expired-result";"0+1"];
    .demo.asyncq.submit .demoasynctest.request["target-result";"0+2"];
    .demo.asyncq.completeDue[];
    .demoasynctest.markExpired "expired-result";
    .demo.asyncq.result "target-result";
    .demoasynctest.assertMatch["result cleans expired jobs";0;count .demo.asyncq.byJobId "expired-result"];

    .demoasynctest.reset[];
    .demo.asyncq.JOB_RETENTION::0D00:01:00.000000000;
    .demo.asyncq.JOBDELAY::0D00:00:00.000000000;
    .demo.asyncq.submit .demoasynctest.request["expired-submit";"0+1"];
    .demo.asyncq.completeDue[];
    .demoasynctest.markExpired "expired-submit";
    .demo.asyncq.submit .demoasynctest.request["target-submit";"0+2"];
    .demoasynctest.assertMatch["submit cleans expired jobs";0;count .demo.asyncq.byJobId "expired-submit"];

    .demoasynctest.reset[];
    .demo.asyncq.JOB_RETENTION::0D00:01:00.000000000;
    .demo.asyncq.JOBDELAY::0D00:00:00.000000000;
    .demo.asyncq.submit .demoasynctest.request["expired-cancel";"0+1"];
    .demo.asyncq.completeDue[];
    .demo.asyncq.JOBDELAY::0D00:01:00.000000000;
    .demo.asyncq.submit .demoasynctest.request["target-cancel";"0+2"];
    .demoasynctest.markExpired "expired-cancel";
    .demo.asyncq.cancel "target-cancel";
    .demoasynctest.assertMatch["cancel cleans expired jobs";0;count .demo.asyncq.byJobId "expired-cancel"];
    (::)
  };

.demoasynctest.testCapacity:{
    .demoasynctest.reset[];
    .demo.asyncq.MAX_RETAINED_JOBS::2;
    .demo.asyncq.JOBDELAY::0D00:00:00.000000000;
    .demo.asyncq.submit .demoasynctest.request["oldest-job";"0+1"];
    .demo.asyncq.submit .demoasynctest.request["newer-job";"0+2"];
    .demo.asyncq.completeDue[];
    oldestRows:.demo.asyncq.byJobId "oldest-job";
    newerRows:.demo.asyncq.byJobId "newer-job";
    .demo.asyncq.JOBS::update finished:.z.p-0D00:10:00.000000000 from .demo.asyncq.JOBS where i in oldestRows;
    .demo.asyncq.JOBS::update finished:.z.p-0D00:05:00.000000000 from .demo.asyncq.JOBS where i in newerRows;
    .demo.asyncq.submit .demoasynctest.request["newest-job";"0+3"];
    .demoasynctest.assertMatch["job cap retains configured count";2;count .demo.asyncq.JOBS];
    .demoasynctest.assertMatch["job cap evicts oldest terminal";0;count .demo.asyncq.byJobId "oldest-job"];
    .demoasynctest.assertMatch["job cap retains newer terminal";1;count .demo.asyncq.byJobId "newer-job"];
    .demoasynctest.assertMatch["job cap retains new live job";1;count .demo.asyncq.byJobId "newest-job"];

    .demoasynctest.reset[];
    .demo.asyncq.MAX_RETAINED_JOBS::2;
    .demo.asyncq.JOBDELAY::0D00:10:00.000000000;
    .demo.asyncq.submit .demoasynctest.request["live-job-aa";"0+1"];
    .demo.asyncq.submit .demoasynctest.request["live-job-bb";"0+2"];
    capacityError:@[.demo.asyncq.submit;.demoasynctest.request["blocked-job";"0+3"];{[err] err}];
    .demoasynctest.assert["live-only capacity fails clearly";(10h=type capacityError) and capacityError like "demo job capacity exhausted*"];
    .demoasynctest.assertMatch["capacity never evicts live jobs";2;count .demo.asyncq.JOBS];
    .demoasynctest.assertMatch["first live job retained";1;count .demo.asyncq.byJobId "live-job-aa"];
    .demoasynctest.assertMatch["second live job retained";1;count .demo.asyncq.byJobId "live-job-bb"];
    (::)
  };

.demoasynctest.testDuplicateCorruption:{
    .demoasynctest.reset[];
    .demo.asyncq.JOBDELAY::0D00:01:00.000000000;
    .demo.asyncq.submit .demoasynctest.request["duplicate-job";"0+1"];
    .demo.asyncq.JOBS::.demo.asyncq.JOBS,.demo.asyncq.JOBS;
    corruptBefore:.demo.asyncq.JOBS;
    expected:"duplicate retained job id: duplicate-job";
    submitError:@[.demo.asyncq.submit;.demoasynctest.request["duplicate-job";"0+2"];{[err] err}];
    statusError:@[.demo.asyncq.status;"duplicate-job";{[err] err}];
    resultError:@[.demo.asyncq.result;"duplicate-job";{[err] err}];
    cancelError:@[.demo.asyncq.cancel;"duplicate-job";{[err] err}];
    timerError:@[.demoasynctest.call0;.demo.asyncq.completeDue;{[err] err}];
    .demoasynctest.assertMatch["submit rejects duplicate retained rows";expected;submitError];
    .demoasynctest.assertMatch["status rejects duplicate retained rows";expected;statusError];
    .demoasynctest.assertMatch["result rejects duplicate retained rows";expected;resultError];
    .demoasynctest.assertMatch["cancel rejects duplicate retained rows";expected;cancelError];
    .demoasynctest.assertMatch["timer rejects duplicate retained rows";expected;timerError];
    corruptAfter:.demo.asyncq.JOBS;
    .demoasynctest.assertMatch["duplicate rejection performs no mutation";corruptBefore;corruptAfter];
    (::)
  };

.demoasynctest.testMalformedAndInvariants:{
    .demoasynctest.reset[];
    .demo.asyncq.JOB_RETENTION::0D00:01:00.000000000;
    .demo.asyncq.JOBDELAY::0D00:00:00.000000000;
    .demo.asyncq.submit .demoasynctest.request["invalid-sentinel";"0+1"];
    .demo.asyncq.completeDue[];
    .demoasynctest.markExpired "invalid-sentinel";

    nonDictError:@[.demo.asyncq.submit;42;{[err] err}];
    badQueryRequest:`RequestID`Query!("bad-query-dict";42);
    badQueryError:@[.demo.asyncq.submit;badQueryRequest;{[err] err}];
    badTextRequest:`RequestID`Query!("bad-query-text";`Query`PanopticonRequestFunction!(42;""));
    badTextError:@[.demo.asyncq.submit;badTextRequest;{[err] err}];
    badFunctionRequest:`RequestID`Query!("bad-function";`Query`PanopticonRequestFunction!("0+1";".demo.asyncq.notTrusted"));
    badFunctionError:@[.demo.asyncq.submit;badFunctionRequest;{[err] err}];
    badIdError:@[.demo.asyncq.submit;.demoasynctest.request[42;"0+1"];{[err] err}];
    spaceIdError:@[.demo.asyncq.submit;.demoasynctest.request["bad id";"0+1"];{[err] err}];
    missingIdRequest:enlist[`Query]!enlist (`Query`PanopticonRequestFunction!("0+1";""));
    missingIdError:@[.demo.asyncq.submit;missingIdRequest;{[err] err}];
    .demoasynctest.assertMatch["non-dictionary request rejected";"demo async submit request must be a dictionary";nonDictError];
    .demoasynctest.assertMatch["non-dictionary Query rejected";"demo async request Query must be a dictionary";badQueryError];
    .demoasynctest.assertMatch["non-text Query.Query rejected";"demo async request Query.Query must be a char vector";badTextError];
    .demoasynctest.assertMatch["untrusted request function rejected";"PanopticonRequestFunction is not trusted";badFunctionError];
    .demoasynctest.assertMatch["non-text RequestID rejected";"job ID must be a char vector";badIdError];
    .demoasynctest.assertMatch["space-bearing RequestID rejected";"job ID must contain only printable ASCII graphic characters (bytes 33..126)";spaceIdError];
    .demoasynctest.assertMatch["missing RequestID rejected";"job ID must not be empty";missingIdError];
    .demoasynctest.assertMatch["malformed submits do not run cleanup";1;count .demo.asyncq.byJobId "invalid-sentinel"];
    cleanupError:@[.demo.asyncq.status;"invalid-sentinel";{[err] err}];
    .demoasynctest.assertMatch["next valid operation performs cleanup";"job not found";cleanupError];

    .demoasynctest.reset[];
    .demo.asyncq.MAX_RETAINED_JOBS::0;
    maxError:@[.demo.asyncq.status;"missing-job";{[err] err}];
    .demoasynctest.assertMatch["invalid max-job config rejected";"demo MAX_RETAINED_JOBS must be a positive integer atom";maxError];

    .demoasynctest.reset[];
    .demo.asyncq.JOB_RETENTION::0;
    retentionError:@[.demo.asyncq.status;"missing-job";{[err] err}];
    .demoasynctest.assertMatch["invalid retention config rejected";"demo JOB_RETENTION must be a non-negative timespan atom";retentionError];

    .demoasynctest.reset[];
    .demo.asyncq.MAX_COMPLETIONS_PER_TICK::0;
    completionError:@[.demoasynctest.call0;.demo.asyncq.completeDue;{[err] err}];
    .demoasynctest.assertMatch["invalid timer cap rejected";"demo MAX_COMPLETIONS_PER_TICK must be a positive integer atom";completionError];

    .demoasynctest.reset[];
    .demo.asyncq.JOBDELAY::-0D00:00:01.000000000;
    delayError:@[.demo.asyncq.submit;.demoasynctest.request["delay-job";"0+1"];{[err] err}];
    .demoasynctest.assertMatch["negative job delay rejected";"demo JOBDELAY must be a non-negative timespan atom";delayError];
    .demoasynctest.assertMatch["invalid delay cannot insert";0;count .demo.asyncq.JOBS];

    .demoasynctest.reset[];
    .demo.asyncq.JOBDELAY::0D00:01:00.000000000;
    .demo.asyncq.submit .demoasynctest.request["row-type-job";"0+1"];
    .demo.asyncq.JOBS::update status:enlist enlist `queued from .demo.asyncq.JOBS;
    rowTypeError:@[.demo.asyncq.status;"row-type-job";{[err] err}];
    .demoasynctest.assert["invalid row type rejected clearly";(10h=type rowTypeError) and rowTypeError like "demo JOBS *"];

    .demoasynctest.reset[];
    .demo.asyncq.JOBDELAY::0D00:00:00.000000000;
    .demo.asyncq.submit .demoasynctest.request["finished-corrupt";"0+1"];
    .demo.asyncq.completeDue[];
    .demo.asyncq.JOBS::update finished:0Np from .demo.asyncq.JOBS;
    finishedBefore:.demo.asyncq.JOBS;
    finishedError:@[.demo.asyncq.status;"finished-corrupt";{[err] err}];
    .demoasynctest.assertMatch["terminal null finished rejected before cleanup";"terminal demo jobs must have a finished timestamp";finishedError];
    .demoasynctest.assertMatch["terminal null finished cannot trigger mutation";finishedBefore;.demo.asyncq.JOBS];

    .demoasynctest.reset[];
    .demo.asyncq.JOBDELAY::0D00:00:00.000000000;
    .demo.asyncq.submit .demoasynctest.request["finished-type-corrupt";"0+1"];
    .demo.asyncq.completeDue[];
    .demo.asyncq.JOBS::update finished:string finished from .demo.asyncq.JOBS;
    finishedTypeBefore:.demo.asyncq.JOBS;
    finishedTypeError:@[.demo.asyncq.status;"finished-type-corrupt";{[err] err}];
    .demoasynctest.assertMatch["terminal corrupt finished type rejected before cleanup";"demo JOBS column types mismatch";finishedTypeError];
    .demoasynctest.assertMatch["terminal corrupt finished type cannot trigger mutation";finishedTypeBefore;.demo.asyncq.JOBS];

    .demoasynctest.reset[];
    .demo.asyncq.JOBS::42;
    tableTypeError:@[.demo.asyncq.status;"missing-job";{[err] err}];
    .demoasynctest.assertMatch["invalid JOBS table type rejected";"demo JOBS must be an unkeyed table";tableTypeError];
    (::)
  };

.demoasynctest.testConfigHardLimits:{
    .demoasynctest.reset[];
    .demo.asyncq.LIVE_JOB_STATUSES::("queued";"running";"queued");
    liveStatusError:@[.demo.asyncq.status;"missing-job";{[err] err}];
    .demoasynctest.assertMatch["live status invariant is exact and bounded";"demo LIVE_JOB_STATUSES invariant mismatch";liveStatusError];

    .demoasynctest.reset[];
    .demo.asyncq.TERMINAL_JOB_STATUSES::("done";"error");
    terminalStatusError:@[.demo.asyncq.status;"missing-job";{[err] err}];
    .demoasynctest.assertMatch["terminal status invariant is exact";"demo TERMINAL_JOB_STATUSES invariant mismatch";terminalStatusError];

    .demoasynctest.reset[];
    .demo.asyncq.JOB_COLUMNS::reverse .demo.asyncq.JOB_COLUMNS;
    columnConstantError:@[.demo.asyncq.status;"missing-job";{[err] err}];
    .demoasynctest.assertMatch["schema column constant is immutable";"demo JOB_COLUMNS invariant mismatch";columnConstantError];

    .demoasynctest.reset[];
    .demo.asyncq.JOB_COLUMN_TYPES::10 0 9 0 0 0 0 0 0 0 12 12 12 0 0h;
    typeConstantError:@[.demo.asyncq.status;"missing-job";{[err] err}];
    .demoasynctest.assertMatch["schema type constant is immutable";"demo JOB_COLUMN_TYPES invariant mismatch";typeConstantError];

    .demoasynctest.reset[];
    .demo.asyncq.JOBDELAY::1D00:00:00.000000001;
    delayCeilingError:@[.demo.asyncq.submit;.demoasynctest.request["delay-ceiling";"0+1"];{[err] err}];
    .demoasynctest.assertMatch["job delay hard ceiling enforced";"demo JOBDELAY exceeds the one-day hard ceiling";delayCeilingError];

    .demoasynctest.reset[];
    .demo.asyncq.JOB_RETENTION::30D00:00:00.000000001;
    retentionCeilingError:@[.demo.asyncq.status;"missing-job";{[err] err}];
    .demoasynctest.assertMatch["retention hard ceiling enforced";"demo JOB_RETENTION exceeds the 30-day hard ceiling";retentionCeilingError];

    .demoasynctest.reset[];
    .demo.asyncq.MAX_RETAINED_JOBS::10001;
    maxCeilingError:@[.demo.asyncq.status;"missing-job";{[err] err}];
    .demoasynctest.assertMatch["retained-job hard ceiling enforced";"demo MAX_RETAINED_JOBS exceeds the 10000-job hard ceiling";maxCeilingError];

    .demoasynctest.reset[];
    .demo.asyncq.MAX_COMPLETIONS_PER_TICK::257;
    completionCeilingError:@[.demoasynctest.call0;.demo.asyncq.completeDue;{[err] err}];
    .demoasynctest.assertMatch["completion hard ceiling enforced";"demo MAX_COMPLETIONS_PER_TICK exceeds the 256-job hard ceiling";completionCeilingError];

    .demoasynctest.reset[];
    .demo.asyncq.JOBDELAY::0D00:01:00.000000000;
    .demo.asyncq.submit .demoasynctest.request["row-ceiling-base";"0+1"];
    .demo.asyncq.JOBS::10001#.demo.asyncq.JOBS;
    rowCeilingError:@[.demo.asyncq.status;"row-ceiling-base";{[err] err}];
    .demoasynctest.assertMatch["scheduler table hard row ceiling enforced";"demo JOBS exceeds the 10000-row hard ceiling";rowCeilingError];
    (::)
  };

.demoasynctest.testTimerCleanupAndBound:{
    .demoasynctest.reset[];
    .demo.asyncq.JOB_RETENTION::0D00:01:00.000000000;
    .demo.asyncq.JOBDELAY::0D00:00:00.000000000;
    .demo.asyncq.submit .demoasynctest.request["timer-expired";"0+1"];
    .demo.asyncq.completeDue[];
    .demo.asyncq.MAX_COMPLETIONS_PER_TICK::2;
    .demo.asyncq.submit .demoasynctest.request["timer-job-aa";"0+1"];
    .demo.asyncq.submit .demoasynctest.request["timer-job-bb";"0+2"];
    .demo.asyncq.submit .demoasynctest.request["timer-job-cc";"0+3"];
    .demoasynctest.markExpired "timer-expired";

    .z.ts[];
    statuses:.demo.asyncq.JOBS`status;
    .demoasynctest.assertMatch["timer removes expired terminal jobs";0;count .demo.asyncq.byJobId "timer-expired"];
    .demoasynctest.assertMatch["timer completion work is capped";2i;sum statuses in .demo.asyncq.TERMINAL_JOB_STATUSES];
    .demoasynctest.assertMatch["timer leaves excess due work queued";1i;sum statuses in .demo.asyncq.LIVE_JOB_STATUSES];

    .z.ts[];
    .demoasynctest.assertMatch["next timer drains remaining bounded work";3i;sum (.demo.asyncq.JOBS`status) in .demo.asyncq.TERMINAL_JOB_STATUSES];
    (::)
  };

.demoasynctest.recordUnexpected:{[name;err;bt]
    .demoasynctest.failures,:enlist name,": unexpected error: ",err,"\n",.Q.sbt bt;
    (::)
  };

.demoasynctest.run:{[name;testFunction]
    .Q.trp[{[fn] fn[]};testFunction;.demoasynctest.recordUnexpected[name;;]];
    (::)
  };

.demoasynctest.run["terminal transitions and payload clearing";.demoasynctest.testTerminalTransitions];
.demoasynctest.run["idempotent submit";.demoasynctest.testIdempotentSubmit];
.demoasynctest.run["result shape round trips";.demoasynctest.testResultRoundTrips];
.demoasynctest.run["public cleanup";.demoasynctest.testPublicCleanup];
.demoasynctest.run["capacity";.demoasynctest.testCapacity];
.demoasynctest.run["duplicate corruption";.demoasynctest.testDuplicateCorruption];
.demoasynctest.run["malformed inputs and invariants";.demoasynctest.testMalformedAndInvariants];
.demoasynctest.run["configuration hard limits";.demoasynctest.testConfigHardLimits];
.demoasynctest.run["timer cleanup and bound";.demoasynctest.testTimerCleanupAndBound];

.demoasynctest.restore[];

if[count .demoasynctest.failures;
  -2 "FAIL asyncq_demo_scheduler_test.q (",string[count .demoasynctest.failures],")\n","\n" sv .demoasynctest.failures;
  exit 1];

-1 "PASS asyncq_demo_scheduler_test.q (9 groups)";
exit 0;
