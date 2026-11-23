//! Bootstrap and instance ownership decisions; credentials and operating-system handles stay in Go.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Request {
    pub op: u8,
    pub elevated: bool,
    pub enable: bool,
    pub authorized: bool,
    pub wait: i64,
    pub shutdown_wait: i64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Reply {
    pub kind: u8,
    pub ok: bool,
    pub flag: bool,
    pub other: bool,
    pub elevated: bool,
    pub connected: bool,
    pub count: i64,
    pub value: i64,
    pub other_value: i64,
    pub token: i64,
}
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Effect {
    pub kind: u8,
    pub count: i64,
    pub value: i64,
    pub flag: bool,
    pub duration: i64,
    pub error: u8,
    pub token: i64,
    pub result_bool: bool,
    pub reuse: bool,
    pub lock: bool,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Error {
    Invalid,
    Done,
}

// Port map. Generated from the Go oracle names; numeric ABI is immutable.
const FAILURE: u8 = 1;
const EXECUTABLE: u8 = 2;
const EXECUTABLE_STAT: u8 = 3;
const RECORD_VALID: u8 = 4;
const SERVICE_REQUEST: u8 = 5;
const BODY_CLOSE: u8 = 6;
const SERVICE_READ: u8 = 7;
const SERVICE_DECODE: u8 = 8;
const RECORD_READ: u8 = 9;
const RECORD_DECODE: u8 = 10;
const SERVICE_FACTS: u8 = 11;
const LOCK_OPEN: u8 = 12;
const DEADLINE: u8 = 13;
const LOCK_TRY: u8 = 14;
const LOCK_ACQUIRED: u8 = 15;
const LOCK_CLOSE: u8 = 16;
const BEFORE: u8 = 17;
const SLEEP: u8 = 18;
const LOCK_RELEASE: u8 = 19;
const LOG: u8 = 20;
const IDENTIFY_CLIENT: u8 = 21;
const IDLE_CLOSE: u8 = 22;
const IDENTIFY_REQUEST: u8 = 23;
const RESPONSE_STATUS: u8 = 24;
const IDENTIFY_DECODE: u8 = 25;
const PROXY_STATE: u8 = 26;
const OWNER_READ: u8 = 27;
const OWNER_ADDRESS: u8 = 28;
const OWNER_KEY: u8 = 29;
const OWNER_KEY_CLOSE: u8 = 30;
const OWNER_DELETE: u8 = 31;
const PROXY_SCRIPT: u8 = 32;
const PROXY_PORT: u8 = 33;
const PROXY_EXECUTE: u8 = 34;
const OWNER_WRITE: u8 = 35;
const BUNDLE_PRESENT: u8 = 36;
const PORTABLE_STAT: u8 = 37;
const PORTABLE_MKDIR: u8 = 38;
const PORTABLE_WRITE: u8 = 39;
const PORTABLE_REMOVE: u8 = 40;
const PORTABLE_SELECT: u8 = 41;
const RECORD_ENCODE: u8 = 42;
const RECORD_TEMP: u8 = 43;
const RECORD_RENAME: u8 = 44;
const RECORD_OWN: u8 = 45;
const RECORD_REMOVE: u8 = 46;
const ANDROID_HOME: u8 = 47;
const CONFIG_HOME: u8 = 48;
const BUNDLE_DIR: u8 = 49;
const DEFAULT_HOME: u8 = 50;
const HOME_MKDIR: u8 = 51;
const OPEN_LOG: u8 = 52;
const ELEVATED: u8 = 53;
const ANNOUNCE: u8 = 54;
const NEW_SERVICE: u8 = 55;
const PREPARE: u8 = 56;
const TOKEN: u8 = 57;
const SERVE: u8 = 58;
const ANDROID_PUBLISH: u8 = 59;
const RECORD_BUILD: u8 = 60;
const SIGNAL_WAIT: u8 = 61;
const SERVER_CLOSE: u8 = 62;
const CLEANUP: u8 = 63;
const SERVICE_LOCK: u8 = 64;
const ANDROID_DETACH: u8 = 65;
const SERVICE_UNLOCK: u8 = 66;
const APP_LOCK: u8 = 67;
const DROP: u8 = 68;
const APP_UNLOCK: u8 = 69;
const BRIDGE_INIT: u8 = 70;
const SOCKET_HOOK: u8 = 71;
const ANDROID_CURRENT: u8 = 72;
const ANDROID_REUSE: u8 = 73;
const TUNNEL_DECODE: u8 = 74;
const TUNNEL_GRANT: u8 = 75;
const TUNNEL_RELEASE: u8 = 76;
const PERMISSION: u8 = 77;
const KERNEL_RUNNING: u8 = 78;
// Ops: 0 join,1 lock,2 running,3 probe,4 handover,5 identity,6 owner,7 identify,
// 8 proxy,9 clear-owner,10 portable,11 write-record,12 remove-record,
// 13 windows,14 android-start,15 android-stop,16 jni-start,17 tunnel.
// 18 takeover is internal only. Terminal=254, done=255.
#[derive(Clone, Default)]
struct Outcome {
    flag: bool,
    reuse: bool,
    lock: bool,
    token: i64,
    value: i64,
    mtime: i64,
}
#[derive(Clone)]
pub struct Flow {
    q: Request,
    e: Effect,
    pc: u16,
    child: Option<Box<Flow>>,
    out: Outcome,
    saved: Reply,
    flag: bool,
}
impl Flow {
    pub fn new(q: Request) -> Result<Self, Error> {
        if q.op > 17 {
            return Err(Error::Invalid);
        }
        Ok(Self::inner(q))
    }
    fn inner(q: Request) -> Self {
        let mut f = Self {
            q,
            e: Effect::default(),
            pc: 0,
            child: None,
            out: Outcome::default(),
            saved: Reply::default(),
            flag: false,
        };
        match f.q.op {
            0 => f.child(2, 100),
            1 => f.emit(LOCK_OPEN, 10),
            2 => f.emit(RECORD_READ, 20),
            3 => f.emit(RECORD_VALID, 30),
            4 => {
                f.emit(SERVICE_REQUEST, 40);
                f.e.count = 1
            }
            5 => f.emit(EXECUTABLE, 50),
            6 => f.emit(PROXY_STATE, 60),
            7 => f.emit(IDENTIFY_CLIENT, 70),
            8 => f.emit(PROXY_SCRIPT, 80),
            9 => f.emit(OWNER_READ, 90),
            10 => f.emit(BUNDLE_PRESENT, 110),
            11 => f.emit(RECORD_ENCODE, 120),
            12 => f.emit(RECORD_READ, 130),
            13 => f.emit(CONFIG_HOME, 140),
            14 => f.emit(ANDROID_HOME, 145),
            15 => f.emit(SERVICE_LOCK, 170),
            16 => f.emit(BRIDGE_INIT, 180),
            17 => f.emit(TUNNEL_DECODE, 190),
            18 => f.emit(SERVICE_FACTS, 200),
            _ => unreachable!(),
        }
        f
    }
    fn emit(&mut self, k: u8, pc: u16) {
        self.e = Effect {
            kind: k,
            ..Default::default()
        };
        self.pc = pc
    }
    fn duration(&mut self, k: u8, pc: u16, d: i64) {
        self.emit(k, pc);
        self.e.duration = d
    }
    fn log(&mut self, n: i64, pc: u16) {
        self.emit(LOG, pc);
        self.e.count = n
    }
    fn failure(&mut self, n: i64) {
        self.emit(FAILURE, 900);
        self.e.count = n
    }
    fn finish(&mut self) {
        self.emit(254, 0);
        self.e.error = u8::from(self.out.token != 0);
        self.e.token = self.out.token;
        self.e.result_bool = self.out.flag;
        self.e.reuse = self.out.reuse;
        self.e.lock = self.out.lock
    }
    fn fail_raw(&mut self, r: &Reply) {
        self.out.token = r.token;
        self.finish()
    }
    fn boolean(&mut self, v: bool) {
        self.out.flag = v;
        self.finish()
    }
    fn child(&mut self, op: u8, pc: u16) {
        self.pc = pc;
        let mut q = self.q.clone();
        q.op = op;
        self.child = Some(Box::new(Self::inner(q)))
    }
    pub fn next(&self) -> &Effect {
        if let Some(c) = &self.child {
            c.next()
        } else {
            &self.e
        }
    }
    pub fn advance(&mut self, r: Reply) -> Result<(), Error> {
        if self.next().kind == 255 {
            return Err(Error::Done);
        }
        if r.kind != self.next().kind {
            return Err(Error::Invalid);
        }
        if self.next().kind == 254 {
            self.emit(255, 0);
            return Ok(());
        }
        if let Some(c) = self.child.as_mut() {
            c.advance(r)?;
            if c.next().kind == 254 {
                let out = c.out.clone();
                self.child = None;
                self.resume(out)?;
            }
            return Ok(());
        }
        match self.pc {
            900 => {
                self.out.token = r.token;
                self.finish()
            }
            10 => {
                if !r.ok {
                    self.fail_raw(&r)
                } else {
                    self.duration(DEADLINE, 11, self.q.wait)
                }
            }
            11 => self.emit(LOCK_TRY, 12),
            12 => {
                if r.ok {
                    self.emit(LOCK_ACQUIRED, 13)
                } else if !r.flag {
                    self.emit(LOCK_CLOSE, 14)
                } else {
                    self.emit(BEFORE, 15)
                }
            }
            13 => {
                self.out.lock = true;
                self.finish()
            }
            14 => self.failure(1),
            15 => {
                if r.flag {
                    self.duration(SLEEP, 11, 50_000_000)
                } else {
                    self.emit(LOCK_CLOSE, 16)
                }
            }
            16 => self.failure(2),
            20 => {
                if r.ok {
                    self.emit(RECORD_DECODE, 21)
                } else {
                    self.boolean(false)
                }
            }
            21 => {
                if r.ok {
                    self.child(3, 22)
                } else {
                    self.boolean(false)
                }
            }
            30 => {
                if r.flag {
                    self.emit(SERVICE_REQUEST, 31)
                } else {
                    self.boolean(false)
                }
            }
            31 => {
                if r.ok {
                    self.emit(SERVICE_READ, 32)
                } else {
                    self.boolean(false)
                }
            }
            32 => {
                if r.count == 200 {
                    self.emit(SERVICE_DECODE, 33)
                } else {
                    self.flag = false;
                    self.emit(BODY_CLOSE, 34)
                }
            }
            33 => {
                self.flag = true;
                self.emit(BODY_CLOSE, 34)
            }
            34 => self.boolean(self.flag),
            40 => {
                if r.ok {
                    self.emit(BODY_CLOSE, 41)
                } else {
                    self.duration(DEADLINE, 42, self.q.shutdown_wait)
                }
            }
            41 => self.duration(DEADLINE, 42, self.q.shutdown_wait),
            42 => self.child(3, 43),
            44 => {
                if r.flag {
                    self.duration(SLEEP, 42, 200_000_000)
                } else {
                    self.boolean(false)
                }
            }
            50 => {
                if r.ok {
                    self.emit(EXECUTABLE_STAT, 51)
                } else {
                    self.finish()
                }
            }
            51 => {
                if r.ok {
                    self.out.value = r.value;
                    self.out.mtime = r.other_value
                }
                self.finish()
            }
            60 => {
                if r.ok && r.flag && r.other {
                    self.emit(OWNER_READ, 61)
                } else {
                    self.boolean(false)
                }
            }
            61 => {
                if r.flag && !r.other {
                    self.emit(OWNER_ADDRESS, 62)
                } else {
                    self.boolean(false)
                }
            }
            62 => {
                if r.flag {
                    self.child(7, 63)
                } else {
                    self.boolean(false)
                }
            }
            70 => self.emit(IDENTIFY_REQUEST, 71),
            71 => {
                if r.ok {
                    self.emit(RESPONSE_STATUS, 72)
                } else {
                    self.flag = false;
                    self.emit(IDLE_CLOSE, 75)
                }
            }
            72 => {
                if r.count == 200 {
                    self.emit(IDENTIFY_DECODE, 73)
                } else {
                    self.flag = false;
                    self.emit(BODY_CLOSE, 74)
                }
            }
            73 => {
                self.flag = r.ok && r.flag;
                self.emit(BODY_CLOSE, 74)
            }
            74 => self.emit(IDLE_CLOSE, 75),
            75 => self.boolean(self.flag),
            80 => {
                if r.ok {
                    self.emit(PROXY_PORT, 81)
                } else {
                    self.emit(79, 86)
                }
            }
            86 => self.emit(PROXY_PORT, 81),
            81 => {
                self.emit(PROXY_EXECUTE, 82);
                self.e.flag = self.q.enable;
                self.e.count = if r.count == 0 { 7890 } else { r.count }
            }
            82 => {
                if !r.ok {
                    self.failure(4)
                } else if self.q.enable {
                    self.emit(OWNER_WRITE, 83)
                } else {
                    self.child(9, 85)
                }
            }
            83 => {
                if r.ok {
                    self.finish()
                } else {
                    self.log(1, 84)
                }
            }
            84 => self.finish(),
            90 => {
                if r.flag && !r.other {
                    self.finish()
                } else {
                    self.emit(OWNER_KEY, 91)
                }
            }
            91 => {
                if r.ok {
                    self.emit(OWNER_DELETE, 92)
                } else {
                    self.finish()
                }
            }
            92 => self.emit(OWNER_KEY_CLOSE, 93),
            93 => self.finish(),
            106 => {
                self.out.reuse = true;
                self.finish()
            }
            107 => self.child(4, 108),
            109 => self.failure(3),
            110 => {
                if r.flag {
                    self.emit(PORTABLE_STAT, 111)
                } else {
                    self.boolean(false)
                }
            }
            111 => {
                if r.ok {
                    self.emit(PORTABLE_MKDIR, 112)
                } else {
                    self.boolean(false)
                }
            }
            112 => {
                if r.ok {
                    self.emit(PORTABLE_WRITE, 113)
                } else {
                    self.log(2, 116)
                }
            }
            113 => {
                if r.ok {
                    self.emit(PORTABLE_REMOVE, 114)
                } else {
                    self.log(3, 116)
                }
            }
            114 => self.emit(PORTABLE_SELECT, 115),
            115 => self.boolean(true),
            116 => self.boolean(false),
            120 => {
                if r.ok {
                    self.emit(RECORD_TEMP, 121)
                } else {
                    self.fail_raw(&r)
                }
            }
            121 => {
                if r.ok {
                    self.emit(RECORD_RENAME, 122)
                } else {
                    self.fail_raw(&r)
                }
            }
            122 => {
                if !r.ok {
                    self.out.token = r.token
                }
                self.finish()
            }
            130 => {
                if r.ok {
                    self.emit(RECORD_DECODE, 131)
                } else {
                    self.finish()
                }
            }
            131 => {
                if r.ok {
                    self.emit(RECORD_OWN, 132)
                } else {
                    self.emit(RECORD_REMOVE, 133)
                }
            }
            132 => {
                if r.flag {
                    self.emit(RECORD_REMOVE, 133)
                } else {
                    self.finish()
                }
            }
            133 => self.finish(),
            140 => {
                if r.ok {
                    self.emit(BUNDLE_DIR, 141)
                } else {
                    self.fail_raw(&r)
                }
            }
            141 => self.emit(DEFAULT_HOME, 142),
            142 => self.child(10, 143),
            145 => self.emit(HOME_MKDIR, 146),
            146 => {
                if r.ok {
                    self.emit(OPEN_LOG, 147)
                } else {
                    self.fail_raw(&r)
                }
            }
            147 => {
                if self.q.op == 14 {
                    self.emit(NEW_SERVICE, 151)
                } else {
                    self.emit(ELEVATED, 148)
                }
            }
            148 => {
                self.q.elevated = r.flag;
                self.log(4, 149)
            }
            149 => self.child(0, 150),
            151 => {
                if r.ok {
                    self.emit(PREPARE, 152)
                } else {
                    self.fail_raw(&r)
                }
            }
            152 => {
                if r.ok {
                    self.emit(TOKEN, 153)
                } else {
                    self.fail_raw(&r)
                }
            }
            153 => {
                if r.ok {
                    self.emit(SERVE, 154)
                } else {
                    self.fail_raw(&r)
                }
            }
            154 => {
                if !r.ok {
                    self.fail_raw(&r)
                } else if self.q.op == 14 {
                    self.emit(ANDROID_PUBLISH, 155)
                } else {
                    self.child(5, 156)
                }
            }
            155 => self.emit(RECORD_BUILD, 157),
            157 => self.child(11, 158),
            159 => self.start_tail(),
            160 => self.finish(),
            161 => self.emit(ANNOUNCE, 162),
            162 => self.emit(SIGNAL_WAIT, 163),
            163 => self.emit(SERVER_CLOSE, 164),
            164 => self.emit(CLEANUP, 165),
            165 => self.child(12, 166),
            167 => self.finish(),
            168 => self.log(5, 169),
            169 => self.finish(),
            170 => self.emit(ANDROID_DETACH, 171),
            171 => {
                self.saved = r;
                self.emit(SERVICE_UNLOCK, 172)
            }
            172 => {
                if self.saved.flag {
                    self.emit(APP_LOCK, 173)
                } else {
                    self.finish()
                }
            }
            173 => self.emit(DROP, 174),
            174 => self.emit(APP_UNLOCK, 175),
            175 => self.emit(CLEANUP, 176),
            176 => {
                if self.saved.other {
                    self.emit(SERVER_CLOSE, 177)
                } else {
                    self.child(12, 178)
                }
            }
            177 => self.child(12, 178),
            180 => {
                if r.flag {
                    self.emit(SOCKET_HOOK, 181)
                } else {
                    self.failure(5)
                }
            }
            181 => self.emit(SERVICE_LOCK, 182),
            182 => self.emit(ANDROID_CURRENT, 183),
            183 => {
                if r.flag {
                    self.emit(ANDROID_REUSE, 184)
                } else {
                    self.child(14, 185)
                }
            }
            184 | 186 => self.emit(SERVICE_UNLOCK, 187),
            187 => self.finish(),
            190 => {
                if r.ok {
                    self.emit(SERVICE_LOCK, 192)
                } else {
                    self.log(9, 191)
                }
            }
            191 => self.emit(SERVICE_LOCK, 192),
            192 => self.emit(ANDROID_CURRENT, 193),
            193 => {
                self.flag = r.flag;
                if r.flag {
                    self.emit(APP_LOCK, 194)
                } else {
                    self.grant()
                }
            }
            194 => self.emit(SERVICE_UNLOCK, 195),
            195 => self.grant(),
            196 => {
                if self.flag {
                    self.emit(PERMISSION, 198);
                    self.e.flag = self.q.authorized;
                } else {
                    self.emit(SERVICE_UNLOCK, 197);
                }
            }
            197 => {
                if self.q.authorized {
                    self.failure(6)
                } else {
                    self.finish()
                }
            }
            198 => self.emit(KERNEL_RUNNING, 199),
            199 => {
                if !self.q.authorized && r.flag {
                    self.emit(DROP, 202)
                } else {
                    self.flag = false;
                    self.emit(APP_UNLOCK, 203)
                }
            }
            202 => {
                self.flag = !r.ok;
                self.emit(APP_UNLOCK, 203)
            }
            203 => {
                if self.flag {
                    self.log(10, 204)
                } else {
                    self.finish()
                }
            }
            204 => self.finish(),
            200 => {
                self.saved = r;
                if self.saved.value != 0 {
                    self.child(5, 205)
                } else {
                    self.boolean(crate::service_logic::takeover(
                        false,
                        self.saved.connected,
                        self.saved.elevated,
                        self.q.elevated,
                    ))
                }
            }
            _ => return Err(Error::Invalid),
        }
        Ok(())
    }
    fn grant(&mut self) {
        self.emit(
            if self.q.authorized {
                TUNNEL_GRANT
            } else {
                TUNNEL_RELEASE
            },
            196,
        )
    }
    fn start_tail(&mut self) {
        if self.q.op == 14 {
            self.log(7, 160)
        } else {
            self.emit(LOCK_RELEASE, 161)
        }
    }
    fn resume(&mut self, o: Outcome) -> Result<(), Error> {
        match self.pc {
            22 | 63 => self.boolean(o.flag),
            43 => {
                if o.flag {
                    self.emit(BEFORE, 44)
                } else {
                    self.boolean(true)
                }
            }
            85 => self.finish(),
            100 => {
                if o.flag {
                    self.child(18, 102)
                } else {
                    self.child(1, 101)
                }
            }
            102 => {
                if o.flag {
                    self.child(1, 101)
                } else {
                    self.out.reuse = true;
                    self.finish()
                }
            }
            101 => {
                if o.token != 0 {
                    self.out.token = o.token;
                    self.finish()
                } else {
                    self.child(2, 103)
                }
            }
            103 => {
                if o.flag {
                    self.child(18, 104)
                } else {
                    self.out.lock = true;
                    self.finish()
                }
            }
            104 => {
                if o.flag {
                    self.log(0, 107)
                } else {
                    self.emit(LOCK_RELEASE, 106)
                }
            }
            108 => {
                if o.flag {
                    self.out.lock = true;
                    self.finish()
                } else {
                    self.emit(LOCK_RELEASE, 109)
                }
            }
            143 => self.emit(HOME_MKDIR, 146),
            150 => {
                if o.token != 0 {
                    self.out.token = o.token;
                    self.emit(ANNOUNCE, 167);
                    self.e.count = 1
                } else if o.reuse {
                    self.emit(ANNOUNCE, 168);
                    self.e.count = 2
                } else {
                    self.emit(NEW_SERVICE, 151)
                }
            }
            156 => self.emit(RECORD_BUILD, 157),
            158 => {
                if o.token != 0 {
                    self.log(6, 159)
                } else {
                    self.start_tail()
                }
            }
            166 | 178 => self.finish(),
            185 => {
                if o.token != 0 {
                    self.out.token = o.token;
                    self.log(8, 186)
                } else {
                    self.emit(SERVICE_UNLOCK, 187)
                }
            }
            205 => {
                let stale = o.value != 0
                    && (o.value != self.saved.value || o.mtime != self.saved.other_value);
                self.boolean(crate::service_logic::takeover(
                    stale,
                    self.saved.connected,
                    self.saved.elevated,
                    self.q.elevated,
                ))
            }
            _ => return Err(Error::Invalid),
        }
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    fn ok() -> Reply {
        Reply {
            ok: true,
            ..Default::default()
        }
    }
    fn run(q: Request, mut reply: impl FnMut(&Effect) -> Reply) -> (Effect, Vec<Effect>) {
        let mut f = Flow::new(q).unwrap();
        let mut t = vec![];
        for _ in 0..200 {
            let e = f.next().clone();
            if e.kind == 254 {
                return (e, t);
            }
            let mut r = reply(&e);
            r.kind = e.kind;
            t.push(e);
            f.advance(r).unwrap()
        }
        panic!("unfinished")
    }
    #[test]
    fn probe_reads_before_status_and_decode_errors_keep_alive() {
        for status in [200, 401] {
            let (r, t) = run(
                Request {
                    op: 3,
                    ..Default::default()
                },
                |e| match e.kind {
                    RECORD_VALID => Reply { flag: true, ..ok() },
                    SERVICE_READ => Reply {
                        count: status,
                        ..ok()
                    },
                    SERVICE_DECODE => Reply {
                        ok: false,
                        ..Default::default()
                    },
                    _ => ok(),
                },
            );
            assert_eq!(r.result_bool, status == 200);
            assert_eq!(t.last().unwrap().kind, BODY_CLOSE);
            assert_eq!(t.iter().any(|e| e.kind == SERVICE_DECODE), status == 200);
        }
    }
    #[test]
    fn join_reuses_without_lock_and_stale_connected_not_takeover() {
        let (r, t) = run(Request::default(), |e| match e.kind {
            RECORD_VALID => Reply { flag: true, ..ok() },
            SERVICE_READ => Reply { count: 200, ..ok() },
            SERVICE_FACTS => Reply {
                value: 5,
                connected: true,
                ..ok()
            },
            EXECUTABLE_STAT => Reply { value: 6, ..ok() },
            _ => ok(),
        });
        assert!(r.reuse);
        assert!(!t.iter().any(|e| e.kind == LOCK_OPEN));
    }
    #[test]
    fn join_second_probe_after_lock_and_failure_releases() {
        let mut reads = 0;
        let (r, t) = run(
            Request {
                op: 0,
                elevated: true,
                wait: 7,
                shutdown_wait: 9,
                ..Default::default()
            },
            |e| match e.kind {
                RECORD_READ => {
                    reads += 1;
                    Reply {
                        ok: reads > 1,
                        ..Default::default()
                    }
                }
                RECORD_VALID => Reply { flag: true, ..ok() },
                SERVICE_READ => Reply { count: 200, ..ok() },
                BEFORE => Reply {
                    flag: false,
                    ..ok()
                },
                FAILURE => Reply {
                    ok: false,
                    token: 77,
                    ..Default::default()
                },
                _ => ok(),
            },
        );
        assert_eq!(r.error, 1);
        assert_eq!(r.token, 77);
        assert!(t.iter().any(|e| e.kind == LOCK_RELEASE));
        assert_eq!(t.last().unwrap().count, 3);
    }
    #[test]
    fn lock_retries_and_closes_before_format_failure() {
        let mut tries = 0;
        let (r, t) = run(
            Request {
                op: 1,
                wait: i64::MAX,
                ..Default::default()
            },
            |e| match e.kind {
                LOCK_TRY => {
                    tries += 1;
                    Reply {
                        ok: false,
                        flag: tries == 1,
                        ..Default::default()
                    }
                }
                BEFORE => Reply { flag: true, ..ok() },
                FAILURE => Reply {
                    ok: false,
                    token: 19,
                    ..Default::default()
                },
                _ => ok(),
            },
        );
        assert_eq!(r.token, 19);
        assert_eq!(
            t.iter().find(|e| e.kind == DEADLINE).unwrap().duration,
            i64::MAX
        );
        assert_eq!(
            t.iter().find(|e| e.kind == SLEEP).unwrap().duration,
            50_000_000
        );
        assert_eq!(t[t.len() - 2].kind, LOCK_CLOSE);
    }
    #[test]
    fn long_lock_loop_has_no_history_or_cap() {
        let mut f = Flow::new(Request {
            op: 1,
            ..Default::default()
        })
        .unwrap();
        for k in [LOCK_OPEN, DEADLINE] {
            f.advance(Reply { kind: k, ..ok() }).unwrap()
        }
        for _ in 0..100001 {
            for r in [
                Reply {
                    kind: LOCK_TRY,
                    ok: false,
                    flag: true,
                    ..Default::default()
                },
                Reply {
                    kind: BEFORE,
                    flag: true,
                    ..ok()
                },
                Reply {
                    kind: SLEEP,
                    ..ok()
                },
            ] {
                f.advance(r).unwrap()
            }
        }
        f.advance(Reply {
            kind: LOCK_TRY,
            ..ok()
        })
        .unwrap();
        f.advance(Reply {
            kind: LOCK_ACQUIRED,
            ..ok()
        })
        .unwrap();
        assert!(f.next().lock);
    }
    #[test]
    fn handover_ignored_post_failure_then_dead_probe_no_wait() {
        let (r, t) = run(
            Request {
                op: 4,
                ..Default::default()
            },
            |e| {
                if e.kind == SERVICE_REQUEST {
                    Reply {
                        ok: false,
                        ..Default::default()
                    }
                } else {
                    ok()
                }
            },
        );
        assert!(r.result_bool);
        assert_eq!(
            t.iter().map(|e| e.kind).collect::<Vec<_>>(),
            [SERVICE_REQUEST, DEADLINE, RECORD_VALID]
        );
    }
    #[test]
    fn identity_errors_zero_but_stat_fullint64_held() {
        let (r, t) = run(
            Request {
                op: 5,
                ..Default::default()
            },
            |_| Reply {
                ok: false,
                ..Default::default()
            },
        );
        assert_eq!(r.error, 0);
        assert_eq!(t.len(), 1);
        let mut f = Flow::new(Request {
            op: 5,
            ..Default::default()
        })
        .unwrap();
        f.advance(Reply {
            kind: EXECUTABLE,
            ..ok()
        })
        .unwrap();
        f.advance(Reply {
            kind: EXECUTABLE_STAT,
            value: i64::MAX,
            other_value: i64::MIN,
            ..ok()
        })
        .unwrap();
        assert_eq!((f.out.value, f.out.mtime), (i64::MAX, i64::MIN));
    }
    #[test]
    fn owner_guard_order_and_identify_cleanup() {
        let (r, t) = run(
            Request {
                op: 6,
                ..Default::default()
            },
            |e| match e.kind {
                PROXY_STATE => Reply {
                    flag: true,
                    other: true,
                    ..ok()
                },
                OWNER_READ | OWNER_ADDRESS => Reply { flag: true, ..ok() },
                RESPONSE_STATUS => Reply { count: 200, ..ok() },
                IDENTIFY_DECODE => Reply { flag: true, ..ok() },
                _ => ok(),
            },
        );
        assert!(r.result_bool);
        assert_eq!(t[t.len() - 2].kind, BODY_CLOSE);
        assert_eq!(t.last().unwrap().kind, IDLE_CLOSE);
    }
    #[test]
    fn clear_owner_foreign_retains_and_malformed_deletes() {
        for foreign in [false, true] {
            let (_, t) = run(
                Request {
                    op: 9,
                    ..Default::default()
                },
                |e| {
                    if e.kind == OWNER_READ {
                        Reply {
                            flag: foreign,
                            ..ok()
                        }
                    } else {
                        ok()
                    }
                },
            );
            assert_eq!(t.iter().any(|e| e.kind == OWNER_DELETE), !foreign);
        }
    }
    #[test]
    fn proxy_zero_port_publish_failure_logonly() {
        let (r, t) = run(
            Request {
                op: 8,
                enable: true,
                ..Default::default()
            },
            |e| {
                if e.kind == OWNER_WRITE {
                    Reply {
                        ok: false,
                        ..Default::default()
                    }
                } else {
                    ok()
                }
            },
        );
        assert_eq!(r.error, 0);
        let e = t.iter().find(|e| e.kind == PROXY_EXECUTE).unwrap();
        assert!(e.flag);
        assert_eq!(e.count, 7890);
        assert_eq!(t.last().unwrap().count, 1);
    }
    #[test]
    fn portable_failure_does_not_select() {
        for fail in [PORTABLE_STAT, PORTABLE_MKDIR, PORTABLE_WRITE] {
            let (r, t) = run(
                Request {
                    op: 10,
                    ..Default::default()
                },
                |e| Reply {
                    flag: true,
                    ok: e.kind != fail,
                    ..Default::default()
                },
            );
            assert!(!r.result_bool);
            assert!(!t.iter().any(|e| e.kind == PORTABLE_SELECT));
        }
    }
    #[test]
    fn record_removal_malformed_allowed_foreign_valid_preserved() {
        for decode in [false, true] {
            let (_, t) = run(
                Request {
                    op: 12,
                    ..Default::default()
                },
                |e| match e.kind {
                    RECORD_DECODE => Reply {
                        ok: decode,
                        ..Default::default()
                    },
                    _ => ok(),
                },
            );
            assert_eq!(t.iter().any(|e| e.kind == RECORD_REMOVE), !decode)
        }
    }
    #[test]
    fn windows_publish_failure_still_announces_waits_and_cleans() {
        let (r, t) = run(
            Request {
                op: 13,
                ..Default::default()
            },
            |e| match e.kind {
                RECORD_READ | RECORD_RENAME => Reply {
                    ok: false,
                    token: 8,
                    ..Default::default()
                },
                _ => ok(),
            },
        );
        assert_eq!(r.error, 0);
        let k: Vec<_> = t.iter().map(|e| e.kind).collect();
        let publish = k.iter().position(|k| *k == RECORD_RENAME).unwrap();
        let release = k.iter().position(|k| *k == LOCK_RELEASE).unwrap();
        let announce = k.iter().position(|k| *k == ANNOUNCE).unwrap();
        assert!(publish < release && release < announce);
        assert!(k.ends_with(&[SERVER_CLOSE, CLEANUP, RECORD_READ]));
    }
    #[test]
    fn android_publishes_before_record_and_stop_order() {
        let (_, t) = run(
            Request {
                op: 14,
                ..Default::default()
            },
            |_| ok(),
        );
        let k: Vec<_> = t.iter().map(|e| e.kind).collect();
        assert!(
            k.iter().position(|k| *k == ANDROID_PUBLISH)
                < k.iter().position(|k| *k == RECORD_BUILD)
        );
        assert!(!k.contains(&EXECUTABLE));
        let (_, t) = run(
            Request {
                op: 15,
                ..Default::default()
            },
            |e| Reply {
                flag: e.kind == ANDROID_DETACH,
                other: true,
                ..ok()
            },
        );
        assert_eq!(
            &t.iter().map(|e| e.kind).collect::<Vec<_>>()[..9],
            [
                SERVICE_LOCK,
                ANDROID_DETACH,
                SERVICE_UNLOCK,
                APP_LOCK,
                DROP,
                APP_UNLOCK,
                CLEANUP,
                SERVER_CLOSE,
                RECORD_READ
            ]
        );
    }
    #[test]
    fn jni_start_error_keeps_original_token_after_unlock() {
        let (r, t) = run(
            Request {
                op: 16,
                ..Default::default()
            },
            |e| match e.kind {
                BRIDGE_INIT => Reply { flag: true, ..ok() },
                HOME_MKDIR => Reply {
                    ok: false,
                    token: 41,
                    ..Default::default()
                },
                _ => ok(),
            },
        );
        assert_eq!(r.token, 41);
        assert_eq!(t[t.len() - 2].count, 8);
        assert_eq!(t.last().unwrap().kind, SERVICE_UNLOCK);
    }
    #[test]
    fn tunnel_grant_no_service_not_undone_and_revoke_drops_connected() {
        for auth in [false, true] {
            let (r, t) = run(
                Request {
                    op: 17,
                    authorized: auth,
                    ..Default::default()
                },
                |e| match e.kind {
                    ANDROID_CURRENT | KERNEL_RUNNING => Reply {
                        flag: !auth,
                        ..ok()
                    },
                    FAILURE => Reply {
                        ok: false,
                        token: 6,
                        ..Default::default()
                    },
                    _ => ok(),
                },
            );
            assert_eq!(r.error, u8::from(auth));
            assert_eq!(t.iter().any(|e| e.kind == DROP), !auth);
            assert_eq!(
                t.iter().filter(|e| e.kind == TUNNEL_RELEASE).count(),
                usize::from(!auth)
            );
        }
    }
    #[test]
    fn wrong_reply_rejected_without_consuming() {
        let mut f = Flow::new(Request {
            op: 1,
            ..Default::default()
        })
        .unwrap();
        let e = f.next().clone();
        assert_eq!(
            f.advance(Reply {
                kind: DEADLINE,
                ..ok()
            }),
            Err(Error::Invalid)
        );
        assert_eq!(f.next(), &e);
    }
}
