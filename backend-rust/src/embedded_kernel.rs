//! Embedded-kernel admission and logging decisions over caller-owned resources.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Request {
    pub op: String,
    pub path: String,
}
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Effect {
    pub kind: String,
    pub text: String,
    pub other: String,
    pub number: i64,
    pub result: String,
}
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Reply {
    pub text: String,
    pub number: i64,
    pub flag: bool,
    pub names: Vec<String>,
    pub bytes: Vec<u8>,
    pub error: Option<String>,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Error {
    Invalid,
    Done,
}

fn quote(s: &str) -> String {
    let mut out = String::from("\"");
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if c < ' ' => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}
fn wrapped(index: usize, verb: &str, value: &str) -> String {
    crate::embedded_kernel_text::TEXT[index].replacen(verb, value, 1)
}
pub struct Flow {
    op: String,
    effect: Effect,
    error: Option<String>,
    status: i64,
    mask: i64,
    done: bool,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, Error> {
        let (kind, number) = match q.op.as_str() {
            "session-start" | "start" => ("chdir", 0),
            "session-stop" => ("stop-monitor", 0),
            "session-apply" => ("check-generation", 0),
            "stop" => ("shutdown", 0),
            "running" => ("lock", 0),
            "refusal" => ("sleep", 200),
            "start-logging" => ("open-log", 0o600),
            "logs" => ("next-event", 0),
            "event" => ("event-facts", 0),
            "stop-logging" => ("lock", 0),
            "check" => ("read-config", 0),
            "apply" => ("parse-live", 0),
            _ => return Err(Error::Invalid),
        };
        Ok(Self {
            op: q.op,
            effect: Effect {
                kind: kind.into(),
                number,
                ..Default::default()
            },
            error: None,
            status: 0,
            mask: 0,
            done: false,
        })
    }
    pub fn next(&self) -> &Effect {
        &self.effect
    }
    fn emit(&mut self, kind: &str, number: i64) {
        self.effect = Effect {
            kind: kind.into(),
            number,
            ..Default::default()
        }
    }
    fn finish(&mut self, error: Option<String>) {
        self.done = true;
        self.effect = Effect {
            kind: "result".into(),
            result: format!(
                "{{\"status\":{},\"text\":\"\",\"data\":null,\"error\":{},\"hasError\":{}}}",
                self.status,
                quote(error.as_deref().unwrap_or("")),
                error.is_some()
            ),
            ..Default::default()
        };
    }
    fn pending_finish(&mut self) {
        let e = self.error.take();
        self.finish(e)
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), Error> {
        if self.done {
            return Err(Error::Done);
        }
        match self.op.as_str() {
            "session-start" | "start" => match self.effect.kind.as_str() {
                "chdir" => {
                    if let Some(e) = r.error {
                        self.finish(Some(wrapped(0, "%w", &e)))
                    } else {
                        self.emit("home", 0)
                    }
                }
                "home" => self.emit("mkdir", 0o700),
                "mkdir" => {
                    if let Some(e) = r.error {
                        self.finish(Some(wrapped(1, "%w", &e)))
                    } else {
                        self.emit("read-config", 0)
                    }
                }
                "read-config" => {
                    if r.error.is_some() {
                        self.finish(Some(crate::embedded_kernel_text::TEXT[4].into()))
                    } else {
                        self.emit("start-logging", 0)
                    }
                }
                "start-logging" => self.emit("hub-parse", 0),
                "hub-parse" => {
                    if let Some(e) = r.error {
                        self.error = Some(wrapped(2, "%w", &e));
                        self.emit("stop-logging", 0)
                    } else {
                        self.emit("refusal", 0)
                    }
                }
                "refusal" => {
                    if let Some(e) = r.error {
                        self.error = Some(e);
                        self.emit("shutdown", 0)
                    } else {
                        self.emit("handover", 0)
                    }
                }
                "shutdown" => self.emit("stop-logging", 0),
                "stop-logging" => self.pending_finish(),
                "handover" => self.emit("lock", 0),
                "lock" => self.emit("set-running", 1),
                "set-running" => self.emit("unlock", 0),
                "unlock" if self.op == "session-start" => self.emit("start-monitor", 0),
                "start-monitor" | "unlock" => self.finish(None),
                _ => return Err(Error::Invalid),
            },
            "session-stop" | "stop" => match self.effect.kind.as_str() {
                "stop-monitor" => self.emit("shutdown", 0),
                "shutdown" => self.emit("stop-logging", 0),
                "stop-logging" => self.emit("lock", 0),
                "lock" => self.emit("set-running", 0),
                "set-running" => self.emit("unlock", 0),
                "unlock" => self.finish(None),
                _ => return Err(Error::Invalid),
            },
            "running" => match self.effect.kind.as_str() {
                "lock" => self.emit("running", 0),
                "running" => {
                    self.status = i64::from(r.flag);
                    self.emit("unlock", 0)
                }
                "unlock" => self.finish(None),
                _ => return Err(Error::Invalid),
            },
            "refusal" => match self.effect.kind.as_str() {
                "sleep" => self.emit("tun-error", 0),
                "tun-error" => {
                    if r.text.is_empty() {
                        self.finish(None)
                    } else {
                        self.finish(Some(wrapped(3, "%s", &r.text)))
                    }
                }
                _ => return Err(Error::Invalid),
            },
            "start-logging" => match self.effect.kind.as_str() {
                "open-log" => {
                    if r.error.is_some() {
                        self.finish(None)
                    } else {
                        self.emit("redactor", 0)
                    }
                }
                "redactor" => self.emit("subscribe", 0),
                "subscribe" => self.emit("lock", 0),
                "lock" => self.emit("publish-log", 0),
                "publish-log" => self.emit("clear-tun-error", 0),
                "clear-tun-error" => self.emit("unlock", 0),
                "unlock" => self.emit("spawn-logs", 0),
                "spawn-logs" => self.finish(None),
                _ => return Err(Error::Invalid),
            },
            "logs" => match self.effect.kind.as_str() {
                "next-event" => {
                    if r.flag {
                        self.emit("handle-event", 0)
                    } else {
                        self.finish(None)
                    }
                }
                "handle-event" => self.emit("next-event", 0),
                _ => return Err(Error::Invalid),
            },
            "event" => match self.effect.kind.as_str() {
                "event-facts" => self.emit(if r.number >= 0 { "lock" } else { "write-log" }, 0),
                "lock" => self.emit("tun-empty", 0),
                "tun-empty" => self.emit(if r.flag { "capture-tun" } else { "unlock" }, 0),
                "capture-tun" => self.emit("unlock", 0),
                "unlock" => self.emit("write-log", 0),
                "write-log" => self.finish(None),
                _ => return Err(Error::Invalid),
            },
            "stop-logging" => match self.effect.kind.as_str() {
                "lock" => self.emit("snapshot-log", 0),
                "snapshot-log" => {
                    self.mask = r.number;
                    self.emit("clear-log", 0)
                }
                "clear-log" => self.emit("unlock", 0),
                "unlock" => {
                    if self.mask & 1 != 0 {
                        self.emit("unsubscribe", 0)
                    } else if self.mask & 2 != 0 {
                        self.emit("close-log", 0)
                    } else {
                        self.finish(None)
                    }
                }
                "unsubscribe" => {
                    if self.mask & 2 != 0 {
                        self.emit("close-log", 0)
                    } else {
                        self.finish(None)
                    }
                }
                "close-log" => self.finish(None),
                _ => return Err(Error::Invalid),
            },
            "check" => match self.effect.kind.as_str() {
                "read-config" => {
                    if r.error.is_some() {
                        self.finish(r.error)
                    } else {
                        self.emit("parse-config", 0)
                    }
                }
                "parse-config" => self.finish(r.error),
                _ => return Err(Error::Invalid),
            },
            "session-apply" | "apply" => match self.effect.kind.as_str() {
                "check-generation" => {
                    if r.error.is_some() {
                        self.finish(r.error)
                    } else {
                        self.emit("parse-live", 0)
                    }
                }
                "parse-live" => {
                    if r.error.is_some() {
                        self.finish(r.error)
                    } else {
                        self.emit("apply-config", 0)
                    }
                }
                "apply-config" => self.finish(None),
                _ => return Err(Error::Invalid),
            },
            _ => return Err(Error::Invalid),
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn new(op: &str) -> Flow {
        Flow::new(Request {
            op: op.into(),
            ..Default::default()
        })
        .unwrap()
    }
    fn answer(f: &mut Flow, k: &str, r: Reply) {
        assert_eq!(f.next().kind, k);
        f.advance(r).unwrap()
    }
    fn ok(f: &mut Flow, k: &str) {
        answer(f, k, Reply::default())
    }
    fn err(s: &str) -> Reply {
        Reply {
            error: Some(s.into()),
            ..Default::default()
        }
    }
    fn success(f: &Flow) {
        assert_eq!(f.next().kind, "result");
        assert!(f.next().result.contains("\"hasError\":false"))
    }
    fn failure(f: &Flow, s: &str) {
        assert_eq!(f.next().kind, "result");
        assert!(
            f.next().result.contains(&format!("\"error\":{}", quote(s))),
            "{}",
            f.next().result
        )
    }
    #[test]
    fn start_preserves_handover_before_running_commit() {
        let mut f = new("start");
        for k in ["chdir", "home"] {
            ok(&mut f, k)
        }
        assert_eq!(f.next().number, 0o700);
        for k in [
            "mkdir",
            "read-config",
            "start-logging",
            "hub-parse",
            "refusal",
            "handover",
            "lock",
        ] {
            ok(&mut f, k)
        }
        assert_eq!(f.next().number, 1);
        ok(&mut f, "set-running");
        ok(&mut f, "unlock");
        success(&f)
    }
    #[test]
    fn start_early_failures_do_not_invent_cleanup() {
        for (at, index) in [("chdir", 0), ("mkdir", 1), ("read-config", 4)] {
            let mut f = new("start");
            while f.next().kind != at {
                f.advance(Reply::default()).unwrap()
            }
            answer(&mut f, at, err("synthetic %w"));
            let expected = if index == 4 {
                crate::embedded_kernel_text::TEXT[4].into()
            } else {
                wrapped(index, "%w", "synthetic %w")
            };
            failure(&f, &expected)
        }
    }
    #[test]
    fn parse_refusal_closes_logging_without_shutdown() {
        let mut f = new("start");
        for k in ["chdir", "home", "mkdir", "read-config", "start-logging"] {
            ok(&mut f, k)
        }
        answer(&mut f, "hub-parse", err("parse error"));
        answer(&mut f, "stop-logging", err("cleanup ignored"));
        failure(&f, &wrapped(2, "%w", "parse error"))
    }
    #[test]
    fn tunnel_refusal_shuts_down_then_stops_logging() {
        let mut f = new("start");
        for k in [
            "chdir",
            "home",
            "mkdir",
            "read-config",
            "start-logging",
            "hub-parse",
        ] {
            ok(&mut f, k)
        }
        answer(&mut f, "refusal", err("original refusal"));
        ok(&mut f, "shutdown");
        ok(&mut f, "stop-logging");
        failure(&f, "original refusal")
    }
    #[test]
    fn stop_orders_shutdown_and_unlock() {
        let mut f = new("stop");
        for k in ["shutdown", "stop-logging", "lock"] {
            ok(&mut f, k)
        }
        assert_eq!(f.next().number, 0);
        ok(&mut f, "set-running");
        ok(&mut f, "unlock");
        success(&f)
    }
    #[test]
    fn running_reads_while_locked_and_returns_after_unlock() {
        for value in [false, true] {
            let mut f = new("running");
            ok(&mut f, "lock");
            answer(
                &mut f,
                "running",
                Reply {
                    flag: value,
                    ..Default::default()
                },
            );
            ok(&mut f, "unlock");
            assert!(f
                .next()
                .result
                .contains(&format!("\"status\":{},", i32::from(value))))
        }
    }
    #[test]
    fn refusal_waits_two_hundred_and_keeps_payload_verbatim() {
        for text in ["", "payload %s \" quote\n"] {
            let mut f = new("refusal");
            assert_eq!(f.next().number, 200);
            ok(&mut f, "sleep");
            answer(
                &mut f,
                "tun-error",
                Reply {
                    text: text.into(),
                    ..Default::default()
                },
            );
            if text.is_empty() {
                success(&f)
            } else {
                failure(&f, &wrapped(3, "%s", text))
            }
        }
    }
    #[test]
    fn failed_log_open_does_not_clear_old_tun_error() {
        let mut f = new("start-logging");
        assert_eq!(f.next().number, 0o600);
        answer(&mut f, "open-log", err("not writable"));
        success(&f)
    }
    #[test]
    fn successful_log_publication_resets_under_lock_before_spawn() {
        let mut f = new("start-logging");
        for k in [
            "open-log",
            "redactor",
            "subscribe",
            "lock",
            "publish-log",
            "clear-tun-error",
            "unlock",
            "spawn-logs",
        ] {
            ok(&mut f, k)
        }
        success(&f)
    }
    #[test]
    fn long_log_loop_has_no_history_growth() {
        let mut f = new("logs");
        let original = std::mem::size_of_val(&f);
        for _ in 0..100_001 {
            answer(
                &mut f,
                "next-event",
                Reply {
                    flag: true,
                    ..Default::default()
                },
            );
            ok(&mut f, "handle-event");
            assert!(f.error.is_none());
            assert_eq!(f.mask, 0)
        }
        assert_eq!(std::mem::size_of_val(&f), original);
        ok(&mut f, "next-event");
        success(&f)
    }
    #[test]
    fn first_tun_error_only_and_log_write_after_unlock() {
        for empty in [false, true] {
            let mut f = new("event");
            answer(
                &mut f,
                "event-facts",
                Reply {
                    number: 4,
                    ..Default::default()
                },
            );
            ok(&mut f, "lock");
            answer(
                &mut f,
                "tun-empty",
                Reply {
                    flag: empty,
                    ..Default::default()
                },
            );
            if empty {
                ok(&mut f, "capture-tun")
            }
            ok(&mut f, "unlock");
            ok(&mut f, "write-log");
            success(&f)
        }
        let mut f = new("event");
        answer(
            &mut f,
            "event-facts",
            Reply {
                number: -1,
                ..Default::default()
            },
        );
        ok(&mut f, "write-log");
        success(&f)
    }
    #[test]
    fn stop_logging_snapshots_and_clears_before_optional_resource_cleanup() {
        for mask in [0, 1, 2, 3, -1] {
            let mut f = new("stop-logging");
            ok(&mut f, "lock");
            answer(
                &mut f,
                "snapshot-log",
                Reply {
                    number: mask,
                    ..Default::default()
                },
            );
            ok(&mut f, "clear-log");
            ok(&mut f, "unlock");
            if mask & 1 != 0 {
                ok(&mut f, "unsubscribe")
            }
            if mask & 2 != 0 {
                ok(&mut f, "close-log")
            }
            success(&f)
        }
    }
    #[test]
    fn config_read_and_parse_errors_are_not_wrapped() {
        let mut f = new("check");
        answer(&mut f, "read-config", err("raw read"));
        failure(&f, "raw read");
        let mut f = new("check");
        ok(&mut f, "read-config");
        answer(&mut f, "parse-config", err("raw parser"));
        failure(&f, "raw parser")
    }
    #[test]
    fn apply_must_parse_before_commit_and_has_no_new_rollback() {
        let mut f = new("apply");
        answer(&mut f, "parse-live", err("bad live"));
        failure(&f, "bad live");
        let mut f = new("apply");
        ok(&mut f, "parse-live");
        answer(&mut f, "apply-config", err("ignored same as original"));
        success(&f)
    }
    #[test]
    fn terminal_and_unknown_operations_reject() {
        assert!(matches!(
            Flow::new(Request {
                op: "bad".into(),
                ..Default::default()
            }),
            Err(Error::Invalid)
        ));
        let mut f = new("check");
        ok(&mut f, "read-config");
        ok(&mut f, "parse-config");
        let last = f.next().clone();
        assert_eq!(f.advance(Reply::default()), Err(Error::Done));
        assert_eq!(f.next(), &last)
    }
}
