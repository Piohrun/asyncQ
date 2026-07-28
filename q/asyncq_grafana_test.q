\l q/asyncq_grafana.q

/ Self-contained regression tests for q/asyncq_grafana.q.
/ Run with at least one secondary thread so the restricted-evaluation mutation
/ test executes outside the main thread. The shell wrapper must also set
/ ulimit -v and pass -T, -w, -u 1, and -b to q.

.asyncqtest.EMPTY_JOBS:0#.grafana.asyncq.JOBS;
.asyncqtest.EMPTY_STREAMS:0#.grafana.asyncq.STREAMS;
.asyncqtest.ORIGINAL_JOB_RETENTION:.grafana.asyncq.JOB_RETENTION;
.asyncqtest.ORIGINAL_MAX_RETAINED_JOBS:.grafana.asyncq.MAX_RETAINED_JOBS;
.asyncqtest.ORIGINAL_MAX_ACTIVE_STREAMS:.grafana.asyncq.MAX_ACTIVE_STREAMS;
.asyncqtest.ORIGINAL_ID_MAX_CHARS:.grafana.asyncq.ID_MAX_CHARS;
.asyncqtest.ORIGINAL_TRUSTED_FUNCTIONS:.grafana.asyncq.TRUSTED_PANOPTICON_FUNCTIONS;
.asyncqtest.failures:();

.asyncqtest.pano:{[req]
    ([] pano:enlist 7)
  };

.asyncqtest.notFunction:42;

.asyncqtest.restore:{
    .grafana.asyncq.JOBS::.asyncqtest.EMPTY_JOBS;
    .grafana.asyncq.STREAMS::.asyncqtest.EMPTY_STREAMS;
    .grafana.asyncq.JOB_RETENTION::.asyncqtest.ORIGINAL_JOB_RETENTION;
    .grafana.asyncq.MAX_RETAINED_JOBS::.asyncqtest.ORIGINAL_MAX_RETAINED_JOBS;
    .grafana.asyncq.MAX_ACTIVE_STREAMS::.asyncqtest.ORIGINAL_MAX_ACTIVE_STREAMS;
    .grafana.asyncq.ID_MAX_CHARS::.asyncqtest.ORIGINAL_ID_MAX_CHARS;
    .grafana.asyncq.TRUSTED_PANOPTICON_FUNCTIONS::.asyncqtest.ORIGINAL_TRUSTED_FUNCTIONS;
    (::)
  };

.asyncqtest.reset:{
    .asyncqtest.restore[];
    .grafana.asyncq.TRUSTED_PANOPTICON_FUNCTIONS::`symbol$();
    (::)
  };

.asyncqtest.assert:{[name;condition]
    if[not 1b~condition; .asyncqtest.failures,:enlist name];
    (::)
  };

.asyncqtest.assertMatch:{[name;expected;actual]
    .asyncqtest.assert[name;expected~actual]
  };

.asyncqtest.request:{[jobId;query]
    `RequestID`Query!(
      jobId;
      `Query`PanopticonRequestFunction!(query;""))
  };

.asyncqtest.panoRequest:{[jobId;query;functionName]
    `RequestID`Query!(
      jobId;
      `Query`PanopticonRequestFunction!(query;functionName))
  };

.asyncqtest.streamRequest:{[streamId;tag]
    `StreamID`Tag!(streamId;tag)
  };

.asyncqtest.assertInvalidJobId:{[caseName;jobId]
    expected:"job ID must contain only printable ASCII graphic characters (bytes 33..126)";
    submitError:@[.grafana.asyncq.async.submit;.asyncqtest.request[jobId;"0+1"];{[err] err}];
    statusError:@[.grafana.asyncq.async.status;jobId;{[err] err}];
    resultError:@[.grafana.asyncq.async.result;jobId;{[err] err}];
    cancelError:@[.grafana.asyncq.async.cancel;jobId;{[err] err}];
    .asyncqtest.assertMatch[(caseName," job ID is rejected by submit");expected;submitError];
    .asyncqtest.assertMatch[(caseName," job ID is rejected by status");expected;statusError];
    .asyncqtest.assertMatch[(caseName," job ID is rejected by result");expected;resultError];
    .asyncqtest.assertMatch[(caseName," job ID is rejected by cancel");expected;cancelError];
    (::)
  };

.asyncqtest.assertInvalidStreamId:{[caseName;streamId]
    expected:"stream ID must contain only printable ASCII graphic characters (bytes 33..126)";
    startError:@[.grafana.asyncq.stream.start;.asyncqtest.streamRequest[streamId;1];{[err] err}];
    stopError:@[.grafana.asyncq.stream.stop;streamId;{[err] err}];
    publishError:.[.grafana.asyncq.stream.publish;(streamId;([] metric:enlist 1));{[err] err}];
    terminalError:.[.grafana.asyncq.stream.error;(streamId;"failed");{[err] err}];
    doneError:@[.grafana.asyncq.stream.done;streamId;{[err] err}];
    .asyncqtest.assertMatch[(caseName," stream ID is rejected by start");expected;startError];
    .asyncqtest.assertMatch[(caseName," stream ID is rejected by stop");expected;stopError];
    .asyncqtest.assertMatch[(caseName," stream ID is rejected by publish");expected;publishError];
    .asyncqtest.assertMatch[(caseName," stream ID is rejected by error");expected;terminalError];
    .asyncqtest.assertMatch[(caseName," stream ID is rejected by done");expected;doneError];
    (::)
  };

.asyncqtest.jobRow:{[jobId]
    rows:.grafana.asyncq.util.byJobId jobId;
    first select from .grafana.asyncq.JOBS where i=first rows
  };

.asyncqtest.testNormalQueryAndResult:{
    .asyncqtest.reset[];
    expected:([] metric:10 20 30);
    submitted:.grafana.asyncq.async.submit .asyncqtest.request["normal";"([] metric:10 20 30)"];
    status:.grafana.asyncq.async.status "normal";
    result:.grafana.asyncq.async.result "normal";
    .asyncqtest.assertMatch["normal submit keeps queued protocol";"queued";submitted`Status];
    .asyncqtest.assertMatch["normal status is done";"done";status`Status];
    .asyncqtest.assertMatch["normal status progress";1f;status`Progress];
    .asyncqtest.assertMatch["normal pure query result";expected;result];
    (::)
  };

.asyncqtest.testDuplicateIdempotency:{
    .asyncqtest.reset[];
    .grafana.asyncq.async.submit .asyncqtest.request["duplicate";"41+1"];
    before:.asyncqtest.jobRow "duplicate";
    duplicateStatus:.grafana.asyncq.async.submit .asyncqtest.request["duplicate";"999"];
    after:.asyncqtest.jobRow "duplicate";
    .asyncqtest.assertMatch["duplicate returns stored status";"done";duplicateStatus`Status];
    .asyncqtest.assertMatch["duplicate retains one row";1;count .grafana.asyncq.JOBS];
    .asyncqtest.assertMatch["duplicate does not re-execute";42;.grafana.asyncq.async.result "duplicate"];
    .asyncqtest.assertMatch["duplicate does not replace row";before;after];

    .grafana.asyncq.JOBS::.grafana.asyncq.JOBS,.grafana.asyncq.JOBS;
    duplicateError:@[.grafana.asyncq.async.status;"duplicate";{[err] err}];
    .asyncqtest.assert["pre-existing duplicate status is explicit";(10h=type duplicateError) and duplicateError like "duplicate retained job id*"];
    duplicateResultError:@[.grafana.asyncq.async.result;"duplicate";{[err] err}];
    .asyncqtest.assert["pre-existing duplicate result is explicit";(10h=type duplicateResultError) and duplicateResultError like "duplicate retained job id*"];
    duplicateCancelError:@[.grafana.asyncq.async.cancel;"duplicate";{[err] err}];
    .asyncqtest.assert["pre-existing duplicate cancel is explicit";(10h=type duplicateCancelError) and duplicateCancelError like "duplicate retained job id*"];
    (::)
  };

.asyncqtest.testRequestClearing:{
    .asyncqtest.reset[];
    successReq:.asyncqtest.request["clear-success";"1+1"];
    .grafana.asyncq.async.submit successReq;
    .asyncqtest.assertMatch["successful job clears request";(::);(.asyncqtest.jobRow "clear-success")`request];

    errorReq:.asyncqtest.request["clear-error";"1+`bad"];
    .grafana.asyncq.async.submit errorReq;
    errorRow:.asyncqtest.jobRow "clear-error";
    .asyncqtest.assertMatch["error job reaches terminal state";"error";errorRow`status];
    .asyncqtest.assertMatch["error job clears request";(::);errorRow`request];

    cancelReq:.asyncqtest.request["clear-cancel";"2+2"];
    .grafana.asyncq.async.submit cancelReq;
    cancelRows:.grafana.asyncq.util.byJobId "clear-cancel";
    .grafana.asyncq.JOBS::update status:enlist "running", request:enlist cancelReq, finished:0Np from .grafana.asyncq.JOBS where i in cancelRows;
    .grafana.asyncq.async.cancel "clear-cancel";
    cancelRow:.asyncqtest.jobRow "clear-cancel";
    .asyncqtest.assertMatch["cancelled job status";"cancelled";cancelRow`status];
    .asyncqtest.assertMatch["cancelled job clears request";(::);cancelRow`request];
    (::)
  };

.asyncqtest.testExpiryCleanup:{
    .asyncqtest.reset[];
    .grafana.asyncq.JOB_RETENTION::0D00:01:00.000000000;
    .grafana.asyncq.async.submit .asyncqtest.request["expired";"1"];
    .grafana.asyncq.async.submit .asyncqtest.request["fresh";"2"];
    expiredRows:.grafana.asyncq.util.byJobId "expired";
    .grafana.asyncq.JOBS::update finished:.z.p-0D00:02:00.000000000 from .grafana.asyncq.JOBS where i in expiredRows;
    .grafana.asyncq.async.status "fresh";
    .asyncqtest.assertMatch["expired terminal job removed";0;count .grafana.asyncq.util.byJobId "expired"];
    .asyncqtest.assertMatch["fresh terminal job retained";1;count .grafana.asyncq.util.byJobId "fresh"];
    (::)
  };

.asyncqtest.testJobCapacity:{
    .asyncqtest.reset[];
    .grafana.asyncq.MAX_RETAINED_JOBS::2;
    .grafana.asyncq.async.submit .asyncqtest.request["oldest";"1"];
    .grafana.asyncq.async.submit .asyncqtest.request["newer";"2"];
    oldestRows:.grafana.asyncq.util.byJobId "oldest";
    newerRows:.grafana.asyncq.util.byJobId "newer";
    .grafana.asyncq.JOBS::update finished:.z.p-0D00:10:00.000000000 from .grafana.asyncq.JOBS where i in oldestRows;
    .grafana.asyncq.JOBS::update finished:.z.p-0D00:05:00.000000000 from .grafana.asyncq.JOBS where i in newerRows;
    .grafana.asyncq.async.submit .asyncqtest.request["newest";"3"];
    .asyncqtest.assertMatch["job cap retains configured count";2;count .grafana.asyncq.JOBS];
    .asyncqtest.assertMatch["job cap evicts oldest terminal";0;count .grafana.asyncq.util.byJobId "oldest"];
    .asyncqtest.assertMatch["job cap retains newer terminal";1;count .grafana.asyncq.util.byJobId "newer"];
    .asyncqtest.assertMatch["job cap retains submitted job";1;count .grafana.asyncq.util.byJobId "newest"];

    .asyncqtest.reset[];
    .grafana.asyncq.MAX_RETAINED_JOBS::2;
    .grafana.asyncq.async.submit .asyncqtest.request["live-a";"1"];
    .grafana.asyncq.async.submit .asyncqtest.request["live-b";"2"];
    .grafana.asyncq.JOBS::update status:enlist "running", finished:0Np from .grafana.asyncq.JOBS;
    capacityError:@[.grafana.asyncq.async.submit;.asyncqtest.request["blocked";"3"];{[err] err}];
    .asyncqtest.assert["live-only job capacity fails clearly";(10h=type capacityError) and capacityError like "job capacity exhausted*"];
    .asyncqtest.assertMatch["live-only capacity never evicts";2;count .grafana.asyncq.JOBS];
    .asyncqtest.assertMatch["first live job retained";1;count .grafana.asyncq.util.byJobId "live-a"];
    .asyncqtest.assertMatch["second live job retained";1;count .grafana.asyncq.util.byJobId "live-b"];
    (::)
  };

.asyncqtest.testIdValidation:{
    .asyncqtest.reset[];
    allowed:"c"$33+til 94;
    .asyncqtest.assertMatch["graphic ASCII alphabet has 94 bytes";94;count allowed];
    .asyncqtest.assertMatch["graphic ASCII alphabet has exact boundaries";"!~";(enlist first allowed),enlist last allowed];
    .asyncqtest.assert["graphic ASCII predicate accepts punctuation";.grafana.asyncq.util.graphicAscii "job:/._-~!@#$%^&*()[]{}=+,;"];
    .asyncqtest.assert["graphic ASCII predicate rejects space";not .grafana.asyncq.util.graphicAscii "a b"];
    .asyncqtest.assert["graphic ASCII predicate rejects newline";not .grafana.asyncq.util.graphicAscii "a\nb"];

    maxId:.grafana.asyncq.ID_MAX_CHARS#"x";
    .grafana.asyncq.async.submit .asyncqtest.request[maxId;"0+1"];
    .asyncqtest.assertMatch["maximum-length job ID is accepted";1;.grafana.asyncq.async.result maxId];

    overlongA:maxId,"a";
    overlongB:maxId,"b";
    overlongErrorA:@[.grafana.asyncq.async.submit;.asyncqtest.request[overlongA;"2"];{[err] err}];
    overlongErrorB:@[.grafana.asyncq.async.submit;.asyncqtest.request[overlongB;"3"];{[err] err}];
    .asyncqtest.assertMatch["first overlong ID is rejected";"job ID exceeds ID_MAX_CHARS";overlongErrorA];
    .asyncqtest.assertMatch["same-prefix overlong ID is independently rejected";"job ID exceeds ID_MAX_CHARS";overlongErrorB];
    .asyncqtest.assertMatch["overlong IDs cannot alias retained prefix";1;count .grafana.asyncq.JOBS];

    punctuationId:"job:/._-~!@#$%^&*()[]{}=+,;";
    .grafana.asyncq.async.submit .asyncqtest.request[punctuationId;"0+2"];
    .asyncqtest.assertMatch["punctuation-rich job ID is accepted unchanged";2;.grafana.asyncq.async.result punctuationId];

    compoundId:enlist[`bad]!enlist 1;
    wrongTypeError:@[.grafana.asyncq.async.submit;.asyncqtest.request[compoundId;"4"];{[err] err}];
    emptyError:@[.grafana.asyncq.async.submit;.asyncqtest.request["";"5"];{[err] err}];
    .asyncqtest.assertMatch["compound job ID is rejected without formatting";"job ID must be a char vector";wrongTypeError];
    .asyncqtest.assertMatch["empty job ID is rejected";"job ID must not be empty";emptyError];

    statusTypeError:@[.grafana.asyncq.async.status;42;{[err] err}];
    resultTypeError:@[.grafana.asyncq.async.result;42;{[err] err}];
    cancelTypeError:@[.grafana.asyncq.async.cancel;42;{[err] err}];
    .asyncqtest.assertMatch["status validates job ID";"job ID must be a char vector";statusTypeError];
    .asyncqtest.assertMatch["result validates job ID";"job ID must be a char vector";resultTypeError];
    .asyncqtest.assertMatch["cancel validates job ID";"job ID must be a char vector";cancelTypeError];

    streamStartTypeError:@[.grafana.asyncq.stream.start;.asyncqtest.streamRequest[42;1];{[err] err}];
    streamStartEmptyError:@[.grafana.asyncq.stream.start;.asyncqtest.streamRequest["";1];{[err] err}];
    streamStartLongError:@[.grafana.asyncq.stream.start;.asyncqtest.streamRequest[overlongA;1];{[err] err}];
    streamStopTypeError:@[.grafana.asyncq.stream.stop;42;{[err] err}];
    streamPublishTypeError:.[.grafana.asyncq.stream.publish;(42;([] metric:enlist 1));{[err] err}];
    streamTerminalErrorTypeError:.[.grafana.asyncq.stream.error;(42;"failed");{[err] err}];
    streamDoneTypeError:@[.grafana.asyncq.stream.done;42;{[err] err}];
    .asyncqtest.assertMatch["stream start validates stream ID";"stream ID must be a char vector";streamStartTypeError];
    .asyncqtest.assertMatch["stream start rejects empty ID";"stream ID must not be empty";streamStartEmptyError];
    .asyncqtest.assertMatch["stream start rejects overlong ID";"stream ID exceeds ID_MAX_CHARS";streamStartLongError];
    .asyncqtest.assertMatch["stream stop validates stream ID";"stream ID must be a char vector";streamStopTypeError];
    .asyncqtest.assertMatch["stream publish validates stream ID";"stream ID must be a char vector";streamPublishTypeError];
    .asyncqtest.assertMatch["stream error validates stream ID";"stream ID must be a char vector";streamTerminalErrorTypeError];
    .asyncqtest.assertMatch["stream done validates stream ID";"stream ID must be a char vector";streamDoneTypeError];

    nulId:"nul",(enlist "c"$0),"id";
    controlId:"control",(enlist "c"$31),"id";
    delId:"del",(enlist "c"$127),"id";
    .asyncqtest.assert["graphic ASCII predicate rejects NUL";not .grafana.asyncq.util.graphicAscii nulId];
    .asyncqtest.assert["graphic ASCII predicate rejects control byte";not .grafana.asyncq.util.graphicAscii controlId];
    .asyncqtest.assert["graphic ASCII predicate rejects DEL";not .grafana.asyncq.util.graphicAscii delId];
    .asyncqtest.assertInvalidJobId["space";"job id"];
    .asyncqtest.assertInvalidJobId["newline";"job\nid"];
    .asyncqtest.assertInvalidJobId["NUL";nulId];
    .asyncqtest.assertInvalidJobId["control";controlId];
    .asyncqtest.assertInvalidJobId["DEL";delId];
    .asyncqtest.assertMatch["unsafe job IDs do not mutate retained jobs";2;count .grafana.asyncq.JOBS];
    .asyncqtest.assertInvalidStreamId["space";"stream id"];
    .asyncqtest.assertInvalidStreamId["newline";"stream\nid"];
    .asyncqtest.assertInvalidStreamId["NUL";nulId];
    .asyncqtest.assertInvalidStreamId["control";controlId];
    .asyncqtest.assertInvalidStreamId["DEL";delId];
    .asyncqtest.assertMatch["unsafe stream IDs do not mutate stream registry";0;count .grafana.asyncq.STREAMS];
    (::)
  };

.asyncqtest.testRequestDictionaryValidation:{
    .asyncqtest.reset[];
    .grafana.asyncq.JOB_RETENTION::0D00:01:00.000000000;
    .grafana.asyncq.async.submit .asyncqtest.request["expired-before-invalid";"1"];
    expiredRows:.grafana.asyncq.util.byJobId "expired-before-invalid";
    .grafana.asyncq.JOBS::update finished:.z.p-0D00:02:00.000000000 from .grafana.asyncq.JOBS where i in expiredRows;

    submitRequestError:@[.grafana.asyncq.async.submit;42;{[err] err}];
    .asyncqtest.assertMatch["submit request type error is deterministic";"async submit request must be a dictionary";submitRequestError];
    .asyncqtest.assertMatch["invalid submit does not mutate retention state";1;count .grafana.asyncq.util.byJobId "expired-before-invalid"];

    missingJobIdReq:enlist[`Query]!enlist (`Query`PanopticonRequestFunction!("0+1";""));
    missingJobIdError:@[.grafana.asyncq.async.submit;missingJobIdReq;{[err] err}];
    .asyncqtest.assertMatch["submit rejects missing RequestID";"job ID must not be empty";missingJobIdError];
    .asyncqtest.assertMatch["missing RequestID does not mutate retention state";1;count .grafana.asyncq.util.byJobId "expired-before-invalid"];

    cleanupError:@[.grafana.asyncq.async.status;"expired-before-invalid";{[err] err}];
    .asyncqtest.assertMatch["next valid job operation still performs cleanup";"job not found";cleanupError];

    .grafana.asyncq.stream.start .asyncqtest.streamRequest["request-sentinel";1];
    streamRequestError:@[.grafana.asyncq.stream.start;42;{[err] err}];
    .asyncqtest.assertMatch["stream start request type error is deterministic";"stream start request must be a dictionary";streamRequestError];
    .asyncqtest.assertMatch["invalid stream request preserves registry";1;count .grafana.asyncq.STREAMS];
    missingStreamIdError:@[.grafana.asyncq.stream.start;enlist[`Tag]!enlist 2;{[err] err}];
    .asyncqtest.assertMatch["stream start rejects missing StreamID and RequestID";"stream ID must not be empty";missingStreamIdError];
    .asyncqtest.assertMatch["missing stream ID preserves registry";1;count .grafana.asyncq.STREAMS];
    (::)
  };

.asyncqtest.testStreamCapacityAndReplacement:{
    .asyncqtest.reset[];
    .grafana.asyncq.MAX_ACTIVE_STREAMS::2;
    .grafana.asyncq.stream.start .asyncqtest.streamRequest["stream-a";1];
    .grafana.asyncq.stream.start .asyncqtest.streamRequest["stream-b";2];
    .grafana.asyncq.stream.start .asyncqtest.streamRequest["stream-a";3];
    replacedRow:first select from .grafana.asyncq.STREAMS where i=first .grafana.asyncq.util.byStreamId "stream-a";
    .asyncqtest.assertMatch["same stream replacement preserves cap";2;count .grafana.asyncq.STREAMS];
    .asyncqtest.assertMatch["same stream replacement stores new request";3;replacedRow[`request;`Tag]];

    streamError:@[.grafana.asyncq.stream.start;.asyncqtest.streamRequest["stream-c";3];{[err] err}];
    .asyncqtest.assert["distinct stream rejected at cap";(10h=type streamError) and streamError like "stream capacity exhausted*"];
    .asyncqtest.assertMatch["stream rejection preserves registry";2;count .grafana.asyncq.STREAMS];

    .grafana.asyncq.stream.stop "stream-b";
    .grafana.asyncq.stream.start .asyncqtest.streamRequest["stream-c";3];
    .asyncqtest.assertMatch["stopped stream frees capacity";2;count .grafana.asyncq.STREAMS];

    .asyncqtest.reset[];
    .grafana.asyncq.stream.start .asyncqtest.streamRequest["corrupt-stream";1];
    .grafana.asyncq.STREAMS::.grafana.asyncq.STREAMS,.grafana.asyncq.STREAMS;
    duplicatePublishError:.[.grafana.asyncq.stream.publish;("corrupt-stream";([] metric:enlist 1));{[err] err}];
    duplicateTerminalError:.[.grafana.asyncq.stream.error;("corrupt-stream";"failed");{[err] err}];
    duplicateDoneError:@[.grafana.asyncq.stream.done;"corrupt-stream";{[err] err}];
    .asyncqtest.assertMatch["publish rejects duplicate active stream rows";"duplicate active stream id: corrupt-stream";duplicatePublishError];
    .asyncqtest.assertMatch["error rejects duplicate active stream rows";"duplicate active stream id: corrupt-stream";duplicateTerminalError];
    .asyncqtest.assertMatch["done rejects duplicate active stream rows";"duplicate active stream id: corrupt-stream";duplicateDoneError];
    .asyncqtest.assertMatch["duplicate rejection preserves corrupt rows";2;count .grafana.asyncq.STREAMS];

    .grafana.asyncq.stream.stop "corrupt-stream";
    .asyncqtest.assertMatch["stop intentionally removes all duplicate stream rows";0;count .grafana.asyncq.STREAMS];

    .grafana.asyncq.stream.start .asyncqtest.streamRequest["replace-corrupt-stream";1];
    .grafana.asyncq.STREAMS::.grafana.asyncq.STREAMS,.grafana.asyncq.STREAMS;
    .grafana.asyncq.stream.start .asyncqtest.streamRequest["replace-corrupt-stream";2];
    replacedCorruptRow:first select from .grafana.asyncq.STREAMS where i=first .grafana.asyncq.util.byStreamId "replace-corrupt-stream";
    .asyncqtest.assertMatch["start intentionally replaces all duplicate stream rows";1;count .grafana.asyncq.STREAMS];
    .asyncqtest.assertMatch["replacement after corruption stores new request";2;replacedCorruptRow[`request;`Tag]];
    (::)
  };

.asyncqtest.testPanopticonFunctionValidation:{
    .asyncqtest.reset[];
    .grafana.asyncq.TRUSTED_PANOPTICON_FUNCTIONS::enlist `.asyncqtest.pano;
    .grafana.asyncq.async.submit .asyncqtest.panoRequest["pano";"ignored";".asyncqtest.pano"];
    .asyncqtest.assertMatch["trusted named Panopticon function executes";([] pano:enlist 7);.grafana.asyncq.async.result "pano"];

    .grafana.asyncq.async.submit .asyncqtest.panoRequest["pano-expression";"ignored";"{[req] 1}"];
    expressionStatus:.grafana.asyncq.async.status "pano-expression";
    .asyncqtest.assertMatch["Panopticon expression is rejected";"error";expressionStatus`Status];
    .asyncqtest.assert["Panopticon expression error is clear";(expressionStatus`Error) like "PanopticonRequestFunction must be*"];

    .grafana.asyncq.TRUSTED_PANOPTICON_FUNCTIONS::enlist `.asyncqtest.notFunction;
    .grafana.asyncq.async.submit .asyncqtest.panoRequest["pano-type";"ignored";".asyncqtest.notFunction"];
    typeStatus:.grafana.asyncq.async.status "pano-type";
    .asyncqtest.assertMatch["non-function trusted name is rejected";"error";typeStatus`Status];
    .asyncqtest.assert["non-function error is clear";(typeStatus`Error) like "trusted Panopticon request name does not resolve*"];
    (::)
  };

.asyncqtest.testRestrictedEvaluation:{
    .asyncqtest.reset[];
    originalLimit:.grafana.asyncq.MAX_RETAINED_JOBS;
    mutationReq:.asyncqtest.request["blocked-mutation";".grafana.asyncq.MAX_RETAINED_JOBS:1"];
    trapped:.grafana.asyncq.util.trapEval peach 4#enlist mutationReq;
    .asyncqtest.assert["blocked mutation is trapped";all not first each trapped];
    .asyncqtest.assert["blocked mutation reports noupdate";all {(x`Error) like "noupdate*"} each last each trapped];
    .asyncqtest.assertMatch["blocked mutation leaves global unchanged";originalLimit;.grafana.asyncq.MAX_RETAINED_JOBS];
    (::)
  };

.asyncqtest.recordUnexpected:{[name;err;bt]
    .asyncqtest.failures,:enlist name,": unexpected error: ",err,"\n",.Q.sbt bt;
    (::)
  };

.asyncqtest.run:{[name;testFunction]
    .Q.trp[{[fn] fn[]};testFunction;.asyncqtest.recordUnexpected[name;;]];
    (::)
  };

.asyncqtest.run["normal query/status/result";.asyncqtest.testNormalQueryAndResult];
.asyncqtest.run["duplicate idempotency";.asyncqtest.testDuplicateIdempotency];
.asyncqtest.run["request clearing";.asyncqtest.testRequestClearing];
.asyncqtest.run["expiry cleanup";.asyncqtest.testExpiryCleanup];
.asyncqtest.run["job capacity";.asyncqtest.testJobCapacity];
.asyncqtest.run["ID validation";.asyncqtest.testIdValidation];
.asyncqtest.run["request dictionary validation";.asyncqtest.testRequestDictionaryValidation];
.asyncqtest.run["stream capacity/replacement";.asyncqtest.testStreamCapacityAndReplacement];
.asyncqtest.run["Panopticon validation";.asyncqtest.testPanopticonFunctionValidation];
.asyncqtest.run["restricted evaluation";.asyncqtest.testRestrictedEvaluation];

.asyncqtest.restore[];

if[count .asyncqtest.failures;
  -2 "FAIL asyncq_grafana_test.q (",string[count .asyncqtest.failures],")\n","\n" sv .asyncqtest.failures;
  exit 1];

-1 "PASS asyncq_grafana_test.q (10 groups)";
exit 0;
