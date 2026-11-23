//! Kernel admission policy. Go retains process, channel, clock and codec resources.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Request {
    pub op: u8,
    pub name: Vec<u8>,
    pub port: i64,
    pub timeout: i64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Reply {
    pub ok: bool,
    pub flag: bool,
    pub other: bool,
    pub text: Vec<u8>,
    pub count: i64,
    pub names: Vec<Vec<u8>>,
    pub token: i64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: Vec<u8>,
    pub text: Vec<u8>,
    pub index: i64,
    pub duration: i64,
    pub error: u8,
    pub token: i64,
    pub flag: bool,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Error {
    Invalid,
    Limit,
    Done,
}

pub struct Flow {
    q: Request,
    effect: Effect,
    pc: u16,
    path: Vec<u8>,
    name: Vec<u8>,
    addr: Vec<u8>,
    pid: Vec<u8>,
    rows: i64,
    row: i64,
    iface: i64,
    context: bool,
    result_error: u8,
    result_token: i64,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, Error> {
        if q.op > 10 {
            return Err(Error::Invalid);
        }
        if q.name.len() > 8 * 1024 * 1024 {
            return Err(Error::Limit);
        }
        let mut s = Self {
            q,
            effect: Effect::default(),
            pc: 0,
            path: vec![],
            name: vec![],
            addr: vec![],
            pid: vec![],
            rows: 0,
            row: 0,
            iface: 0,
            context: false,
            result_error: 0,
            result_token: 0,
        };
        match s.q.op {
            0 | 2 | 10 => s.emit(1, b"saved-path"),
            1 => {
                s.name = s.q.name.clone();
                s.emit(2, b"bundle-present");
            }
            3 | 6 => s.emit(10, b"running"),
            4 => s.emit(20, b"command-new"),
            5 => s.emit(30, b"process-wait"),
            7 => {
                s.emit(40, b"deadline");
                s.effect.duration = s.q.timeout;
            }
            8 => {
                s.emit(50, b"context");
                s.effect.duration = 5_000_000_000;
            }
            9 => s.emit(60, b"interface"),
            _ => unreachable!(),
        }
        Ok(s)
    }
    pub fn next(&self) -> &Effect {
        &self.effect
    }
    fn emit(&mut self, pc: u16, kind: &[u8]) {
        self.pc = pc;
        self.effect = Effect {
            kind: kind.to_vec(),
            ..Effect::default()
        };
    }
    fn formatted(&mut self, code: i64, text: Vec<u8>) {
        self.emit(90, b"error");
        self.effect.index = code;
        self.effect.text = text;
    }
    fn finish(&mut self, error: u8, token: i64) {
        self.result_error = error;
        self.result_token = token;
        if self.context {
            self.context = false;
            self.emit(91, b"context-cancel");
        } else {
            self.terminal();
        }
    }
    fn terminal(&mut self) {
        self.emit(254, b"result");
        self.effect.error = self.result_error;
        self.effect.token = self.result_token;
    }
    fn path_done(&mut self) {
        match self.q.op {
            0 | 1 => {
                self.finish(0, 0);
                self.effect.text = self.path.clone();
            }
            2 => {
                self.emit(6, b"stat");
                self.effect.text = self.path.clone();
            }
            10 => {
                if self.path.is_empty() {
                    self.finish(3, 0)
                } else {
                    self.emit(70, b"context");
                    self.effect.duration = 20_000_000_000;
                }
            }
            _ => unreachable!(),
        }
    }
    fn next_row(&mut self) {
        if self.row >= self.rows {
            self.formatted(5, self.addr.clone());
        } else {
            self.emit(54, b"row");
            self.effect.index = self.row;
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), Error> {
        if self.pc == 255 {
            return Err(Error::Done);
        }
        if r.text.len() > 8 * 1024 * 1024
            || r.names.len() > 100_000
            || r.names.iter().map(Vec::len).sum::<usize>() > 8 * 1024 * 1024
        {
            return Err(Error::Limit);
        }
        // Failed authority reads are not empty paths. Reject before consuming.
        if self.pc == 1 && !r.ok {
            return Err(Error::Invalid);
        }
        match self.pc {
            1 => {
                if r.text.is_empty() {
                    self.name = b"mihomo.exe".to_vec();
                    self.emit(2, b"bundle-present");
                } else {
                    self.path = r.text;
                    self.path_done();
                }
            }
            2 => {
                if r.flag {
                    self.emit(3, b"bundle-path");
                } else {
                    self.emit(5, b"home-path");
                }
                self.effect.text = self.name.clone();
            }
            3 => {
                self.path = r.text;
                self.emit(4, b"stat");
                self.effect.text = self.path.clone();
            }
            4 => {
                if r.ok && !r.flag {
                    self.path_done();
                } else {
                    self.emit(5, b"home-path");
                    self.effect.text = self.name.clone();
                }
            }
            5 => {
                self.path = r.text;
                self.path_done();
            }
            6 => {
                if !r.ok {
                    self.formatted(1, self.path.clone());
                } else {
                    self.emit(7, b"runner-new");
                    self.effect.text = self.path.clone();
                }
            }
            7 => self.finish(u8::from(!r.ok), if r.ok { 0 } else { r.token }),
            10 => {
                if self.q.op == 3 {
                    self.finish(0, 0);
                    self.effect.flag = r.flag;
                } else if r.flag {
                    self.emit(11, b"process-kill");
                } else {
                    self.finish(0, 0);
                }
            }
            11 => {
                if r.ok {
                    self.emit(12, b"exited-join");
                } else {
                    self.finish(1, r.token);
                }
            }
            12 => self.finish(0, 0),
            20 => self.emit(21, b"log-open"),
            21 => {
                if r.ok {
                    self.emit(22, b"command-log");
                } else {
                    self.formatted(2, vec![]);
                }
            }
            22 => self.emit(23, b"process-start"),
            23 => {
                if r.ok {
                    self.emit(25, b"publish");
                } else {
                    self.emit(24, b"log-close");
                }
            }
            24 => self.formatted(3, vec![]),
            25 => self.emit(26, b"exited-new"),
            26 => self.emit(27, b"watch-spawn"),
            27 => self.finish(0, 0),
            30 => self.emit(31, b"log-close"),
            31 => self.emit(32, b"exited-close"),
            32 => self.emit(33, b"exit-callback"),
            33 => self.finish(0, 0),
            40 => self.emit(41, b"before"),
            41 => {
                if r.flag {
                    self.emit(42, b"dial");
                    self.effect.duration = 300_000_000;
                } else {
                    self.finish(2, 0);
                }
            }
            42 => {
                if r.ok {
                    self.emit(43, b"conn-close");
                } else {
                    self.emit(44, b"sleep");
                    self.effect.duration = 50_000_000;
                }
            }
            43 => self.finish(0, 0),
            44 => self.emit(41, b"before"),
            50 => {
                self.context = true;
                self.emit(51, b"netstat");
            }
            51 => {
                if r.ok {
                    self.emit(52, b"row-count");
                } else {
                    self.formatted(4, vec![]);
                }
            }
            52 => {
                self.rows = r.count;
                self.emit(53, b"owner-address");
                self.effect.index = self.q.port;
            }
            53 => {
                self.addr = r.text;
                self.next_row();
            }
            54 => {
                if r.names.len() == 5 && r.names[1] == self.addr && r.names[2] == b"0.0.0.0:0" {
                    self.pid = r.names[4].clone();
                    self.emit(55, b"pid");
                } else {
                    self.row += 1;
                    self.next_row();
                }
            }
            55 => {
                if r.text == self.pid {
                    self.finish(0, 0);
                } else {
                    self.row += 1;
                    self.next_row();
                }
            }
            60 => {
                if !r.ok || !r.flag {
                    self.formatted(6, vec![]);
                } else {
                    self.iface = r.count;
                    self.emit(61, b"route4");
                }
            }
            61 => {
                if !r.ok || r.count != self.iface {
                    self.formatted(7, vec![]);
                } else {
                    self.emit(62, b"route6");
                }
            }
            62 => {
                if r.flag || (r.ok && r.count == self.iface) {
                    self.finish(0, 0);
                } else {
                    self.formatted(8, vec![]);
                }
            }
            70 => {
                self.context = true;
                self.emit(71, b"config-command");
                self.effect.text = self.path.clone();
            }
            71 => {
                if r.ok {
                    self.finish(0, 0);
                } else {
                    self.emit(72, b"output-split");
                }
            }
            72 => {
                let mut tail = r.names.last().cloned().unwrap_or_default();
                if tail.len() > 300 {
                    tail.truncate(300);
                    tail.extend_from_slice("\u{2026}".as_bytes());
                }
                self.formatted(9, tail);
            }
            90 => self.finish(u8::from(!r.ok), if r.ok { 0 } else { r.token }),
            91 => self.terminal(),
            254 => self.emit(255, b"done"),
            _ => return Err(Error::Invalid),
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn r() -> Reply {
        Reply {
            ok: true,
            ..Reply::default()
        }
    }
    fn flow(op: u8) -> Flow {
        Flow::new(Request {
            op,
            ..Request::default()
        })
        .unwrap()
    }
    fn step(f: &mut Flow, k: &str, r: Reply) {
        assert_eq!(f.next().kind, k.as_bytes());
        f.advance(r).unwrap();
    }
    #[test]
    fn saved_path_skips_stat_and_look() {
        let mut f = flow(0);
        step(
            &mut f,
            "saved-path",
            Reply {
                text: b"x".to_vec(),
                ..r()
            },
        );
        assert_eq!(f.next().text, b"x");
        assert_eq!(f.next().kind, b"result");
    }
    #[test]
    fn directory_bundle_falls_back() {
        let mut f = flow(1);
        step(&mut f, "bundle-present", Reply { flag: true, ..r() });
        step(
            &mut f,
            "bundle-path",
            Reply {
                text: b"b".to_vec(),
                ..r()
            },
        );
        step(&mut f, "stat", Reply { flag: true, ..r() });
        step(
            &mut f,
            "home-path",
            Reply {
                text: b"h".to_vec(),
                ..r()
            },
        );
        assert_eq!(f.next().text, b"h");
    }
    #[test]
    fn new_accepts_directory_stat_and_returns_original_error() {
        let mut f = flow(2);
        step(
            &mut f,
            "saved-path",
            Reply {
                text: b"p".to_vec(),
                ..r()
            },
        );
        step(&mut f, "stat", Reply { flag: true, ..r() });
        step(
            &mut f,
            "runner-new",
            Reply {
                ok: false,
                token: 77,
                ..r()
            },
        );
        assert_eq!(f.next().token, 77);
    }
    #[test]
    fn start_failure_closes_only_new_log() {
        let mut f = flow(4);
        for k in ["command-new", "log-open", "command-log"] {
            step(&mut f, k, r());
        }
        step(&mut f, "process-start", Reply { ok: false, ..r() });
        step(&mut f, "log-close", Reply { ok: false, ..r() });
        assert_eq!(f.next().index, 3);
        step(
            &mut f,
            "error",
            Reply {
                ok: false,
                token: 4,
                ..r()
            },
        );
        assert_eq!(f.next().error, 1);
    }
    #[test]
    fn start_publish_before_channel_before_spawn() {
        let mut f = flow(4);
        for k in [
            "command-new",
            "log-open",
            "command-log",
            "process-start",
            "publish",
            "exited-new",
            "watch-spawn",
        ] {
            step(&mut f, k, r());
        }
        assert_eq!(f.next().kind, b"result");
    }
    #[test]
    fn watch_ignores_wait_and_close_errors() {
        let mut f = flow(5);
        for k in ["process-wait", "log-close", "exited-close", "exit-callback"] {
            step(&mut f, k, Reply { ok: false, ..r() });
        }
        assert_eq!(f.next().error, 0);
    }
    #[test]
    fn kill_error_does_not_join() {
        let mut f = flow(6);
        step(&mut f, "running", Reply { flag: true, ..r() });
        step(
            &mut f,
            "process-kill",
            Reply {
                ok: false,
                token: 3,
                ..r()
            },
        );
        assert_eq!(f.next().kind, b"result");
        assert_eq!(f.next().token, 3);
    }
    #[test]
    fn expired_ready_never_dials() {
        let mut f = flow(7);
        step(&mut f, "deadline", r());
        step(&mut f, "before", r());
        assert_eq!(f.next().error, 2);
    }
    #[test]
    fn ready_long_loop_no_history_and_success_close_once() {
        let mut f = flow(7);
        step(&mut f, "deadline", r());
        for _ in 0..100_001 {
            step(&mut f, "before", Reply { flag: true, ..r() });
            assert_eq!(f.next().duration, 300_000_000);
            step(&mut f, "dial", Reply { ok: false, ..r() });
            assert_eq!(f.next().duration, 50_000_000);
            step(&mut f, "sleep", r());
        }
        step(&mut f, "before", Reply { flag: true, ..r() });
        step(&mut f, "dial", r());
        step(&mut f, "conn-close", Reply { ok: false, ..r() });
        assert_eq!(f.next().error, 0);
    }
    #[test]
    fn owner_ignores_protocol_and_state_but_delays_pid() {
        let mut f = flow(8);
        assert_eq!(f.next().duration, 5_000_000_000);
        step(&mut f, "context", r());
        step(&mut f, "netstat", r());
        step(&mut f, "row-count", Reply { count: 2, ..r() });
        step(
            &mut f,
            "owner-address",
            Reply {
                text: b"a".to_vec(),
                ..r()
            },
        );
        step(
            &mut f,
            "row",
            Reply {
                names: vec![b"junk".to_vec()],
                ..r()
            },
        );
        assert_eq!(f.next().index, 1);
        step(
            &mut f,
            "row",
            Reply {
                names: ["UDP", "a", "0.0.0.0:0", "NOT_LISTENING", "42"]
                    .map(|s| s.as_bytes().to_vec())
                    .to_vec(),
                ..r()
            },
        );
        step(
            &mut f,
            "pid",
            Reply {
                text: b"42".to_vec(),
                ..r()
            },
        );
        step(&mut f, "context-cancel", r());
        assert_eq!(f.next().error, 0);
    }
    #[test]
    fn owner_error_kept_across_cancel_error() {
        let mut f = flow(8);
        step(&mut f, "context", r());
        step(&mut f, "netstat", Reply { ok: false, ..r() });
        assert_eq!(f.next().index, 4);
        step(
            &mut f,
            "error",
            Reply {
                ok: false,
                token: 8,
                ..r()
            },
        );
        step(
            &mut f,
            "context-cancel",
            Reply {
                ok: false,
                token: 9,
                ..r()
            },
        );
        assert_eq!(f.next().token, 8);
    }
    #[test]
    fn privacy_allowed_ipv6_error_wins() {
        let mut f = flow(9);
        step(
            &mut f,
            "interface",
            Reply {
                flag: true,
                count: 7,
                ..r()
            },
        );
        step(&mut f, "route4", Reply { count: 7, ..r() });
        step(
            &mut f,
            "route6",
            Reply {
                ok: false,
                flag: true,
                count: 0,
                ..r()
            },
        );
        assert_eq!(f.next().error, 0);
    }
    #[test]
    fn privacy_ipv4_mismatch_stops() {
        let mut f = flow(9);
        step(
            &mut f,
            "interface",
            Reply {
                flag: true,
                count: 7,
                ..r()
            },
        );
        step(&mut f, "route4", Reply { count: 8, ..r() });
        assert_eq!(f.next().kind, b"error");
        assert_eq!(f.next().index, 7);
    }
    #[test]
    fn config_20s_format_then_cancel() {
        let mut f = flow(10);
        step(
            &mut f,
            "saved-path",
            Reply {
                text: b"p".to_vec(),
                ..r()
            },
        );
        assert_eq!(f.next().duration, 20_000_000_000);
        step(&mut f, "context", r());
        step(&mut f, "config-command", Reply { ok: false, ..r() });
        step(
            &mut f,
            "output-split",
            Reply {
                names: vec![b"first".to_vec(), vec![0xff; 301]],
                ..r()
            },
        );
        assert_eq!(f.next().text.len(), 303);
        assert_eq!(&f.next().text[..300], &[0xff; 300]);
        assert_eq!(f.next().index, 9);
        step(
            &mut f,
            "error",
            Reply {
                ok: false,
                token: 1,
                ..r()
            },
        );
        step(&mut f, "context-cancel", r());
        assert_eq!(f.next().token, 1);
    }
    #[test]
    fn authority_reject_does_not_consume() {
        let mut f = flow(0);
        let old = f.next().clone();
        assert_eq!(f.advance(Reply { ok: false, ..r() }), Err(Error::Invalid));
        assert_eq!(*f.next(), old);
    }
}
