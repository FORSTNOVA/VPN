# Phase31: ordinary settings, selection and pin authority

This phase moves ordinary settings/traffic policy, selected node/mode, persistent pins and runtime pins into the same long-lived Rust authority as traffic, path and health/region/job state. The two pin sets remain distinct. ABI v4 requires capability mask 15; default Go and pre-selection whole-instance Go fallback for older DLLs remain.

The entire current Chain and named Chains map remain Go-owned resource graphs, including map/slice identity, capacity, external alias writes and rollback. No old identity assertion is weakened. Arbitrary Proxy graphs also remain Go-owned. A settings document temporarily joins Rust control/path values and these Go graph references. This is not a claim that the chain/catalog graph or standalone backend is entirely Rust.

Control changes use typed presence masks and explicit load/decode boundaries, not stale whole-snapshot replacement. Partial JSON decoding remains Go encoding/json behavior, including the fields changed before an error. Ordinary settings save failure retains memory; chain save failure restores the original Go graph references without reverting unrelated control. Subscription/catalog effects must issue only their own URL/hours/selection intentions.

Pending or unavailable authority fails closed; uncertain mutation is not replayed. Checkpoint/revision/Ack spans four domains. Go retains HTTP transport, OS/Mihomo resources, clocks, workers, persistence adapters and codecs. Actual validation and delivery status are recorded separately. No production cutover or real network/platform acceptance is implied.

Focused verification: 548 Rust tests (421 unit and 127 integration/ABI); 1,800 four-domain randomized steps; 36 control-field delivery faults and 18 checkpoint corruptions; 10,000-item list with 5,000 scalar updates and narrow queries. Actual greater-than-64MiB checkpoint output and cumulative 8MiB control boundaries are covered. Independent review repaired chain save-panic graph rollback and explicit same-value merged-URL intent. Frozen aggregate, original-project retest and delivery results are recorded separately.
