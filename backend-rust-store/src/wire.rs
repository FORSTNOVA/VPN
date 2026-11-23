//! Bounded binary codec. Parse all input before calling a typed Store method.
use crate::raw::*;
use crate::raw_traffic::*;
use crate::{Error, Store};
const MAX_INPUT: usize = 8 * 1024 * 1024;
const MAX_OUTPUT: usize = 16 * 1024 * 1024 - 20;
const MAX_ITEMS: usize = 100_000;
pub struct Reply {
    pub category: u32,
    pub sqlcode: u32,
    pub outcome: u32,
    pub payload: Vec<u8>,
}
impl Reply {
    fn invalid() -> Self {
        Self {
            category: 1,
            sqlcode: 0,
            outcome: 0,
            payload: vec![],
        }
    }
    fn ok(payload: Vec<u8>) -> Self {
        Self {
            category: 0,
            sqlcode: 0,
            outcome: 1,
            payload,
        }
    }
}
fn error(e: Error, mutating: bool, payload: Vec<u8>) -> Reply {
    let (category, sqlcode) = match e {
        Error::Sql(rusqlite::Error::SqliteFailure(e, _)) => (2, e.extended_code as u32),
        Error::Sql(_) => (2, 0),
        Error::Closed | Error::Poisoned => (3, 0),
        _ => (4, 0),
    };
    Reply {
        category,
        sqlcode,
        outcome: if mutating { 2 } else { 0 },
        payload,
    }
}
struct Reader<'a> {
    b: &'a [u8],
    p: usize,
}
impl<'a> Reader<'a> {
    fn take(&mut self, n: usize) -> Result<&'a [u8], ()> {
        let end = self.p.checked_add(n).ok_or(())?;
        let s = self.b.get(self.p..end).ok_or(())?;
        self.p = end;
        Ok(s)
    }
    fn finish(&self) -> Result<(), ()> {
        if self.p == self.b.len() {
            Ok(())
        } else {
            Err(())
        }
    }
}
struct Writer(Vec<u8>);
impl Writer {
    fn put(&mut self, b: &[u8]) -> Result<(), ()> {
        if self.0.len().checked_add(b.len()).ok_or(())? > MAX_OUTPUT {
            return Err(());
        }
        self.0.extend_from_slice(b);
        Ok(())
    }
}
trait Codec: Sized {
    fn read(r: &mut Reader<'_>) -> Result<Self, ()>;
    fn write(&self, w: &mut Writer) -> Result<(), ()>;
}
impl Codec for i64 {
    fn read(r: &mut Reader<'_>) -> Result<Self, ()> {
        Ok(i64::from_le_bytes(r.take(8)?.try_into().map_err(|_| ())?))
    }
    fn write(&self, w: &mut Writer) -> Result<(), ()> {
        w.put(&self.to_le_bytes())
    }
}
impl Codec for u64 {
    fn read(r: &mut Reader<'_>) -> Result<Self, ()> {
        Ok(u64::from_le_bytes(r.take(8)?.try_into().map_err(|_| ())?))
    }
    fn write(&self, w: &mut Writer) -> Result<(), ()> {
        w.put(&self.to_le_bytes())
    }
}
impl Codec for bool {
    fn read(r: &mut Reader<'_>) -> Result<Self, ()> {
        match r.take(1)?[0] {
            0 => Ok(false),
            1 => Ok(true),
            _ => Err(()),
        }
    }
    fn write(&self, w: &mut Writer) -> Result<(), ()> {
        w.put(&[u8::from(*self)])
    }
}
impl Codec for RawText {
    fn read(r: &mut Reader<'_>) -> Result<Self, ()> {
        let n = usize::try_from(u64::read(r)?).map_err(|_| ())?;
        Ok(Self(r.take(n)?.to_vec()))
    }
    fn write(&self, w: &mut Writer) -> Result<(), ()> {
        (self.0.len() as u64).write(w)?;
        w.put(&self.0)
    }
}
impl<T: Codec> Codec for Vec<T> {
    fn read(r: &mut Reader<'_>) -> Result<Self, ()> {
        let n = usize::try_from(u64::read(r)?).map_err(|_| ())?;
        if n > MAX_ITEMS {
            return Err(());
        }
        let mut v = Vec::new();
        for _ in 0..n {
            v.push(T::read(r)?);
        }
        Ok(v)
    }
    fn write(&self, w: &mut Writer) -> Result<(), ()> {
        if self.len() > MAX_ITEMS {
            return Err(());
        }
        (self.len() as u64).write(w)?;
        for v in self {
            v.write(w)?;
        }
        Ok(())
    }
}
fn encoded<T: Codec>(v: &T) -> Result<Vec<u8>, ()> {
    let mut w = Writer(Vec::new());
    v.write(&mut w)?;
    Ok(w.0)
}
fn read_reply<T: Codec>(r: crate::Result<T>) -> Reply {
    match r {
        Ok(v) => match encoded(&v) {
            Ok(b) => Reply::ok(b),
            Err(_) => Reply {
                category: 4,
                sqlcode: 0,
                outcome: 0,
                payload: vec![],
            },
        },
        Err(e) => error(e, false, vec![]),
    }
}
fn write_reply(r: crate::Result<()>) -> Reply {
    match r {
        Ok(()) => Reply::ok(vec![]),
        Err(e) => error(e, true, vec![]),
    }
}
impl Codec for RawNode {
    fn read(r: &mut Reader<'_>) -> Result<Self, ()> {
        Ok(Self {
            name: <RawText>::read(r)?,
            protocol: <RawText>::read(r)?,
            network: <RawText>::read(r)?,
            tls: <bool>::read(r)?,
        })
    }
    fn write(&self, w: &mut Writer) -> Result<(), ()> {
        self.name.write(w)?;
        self.protocol.write(w)?;
        self.network.write(w)?;
        self.tls.write(w)?;
        Ok(())
    }
}
impl Codec for RawProbeRow {
    fn read(r: &mut Reader<'_>) -> Result<Self, ()> {
        Ok(Self {
            path: <RawText>::read(r)?,
            fingerprint: <RawText>::read(r)?,
            delay: <i64>::read(r)?,
            error: <RawText>::read(r)?,
            measured: <i64>::read(r)?,
            details: <RawText>::read(r)?,
        })
    }
    fn write(&self, w: &mut Writer) -> Result<(), ()> {
        self.path.write(w)?;
        self.fingerprint.write(w)?;
        self.delay.write(w)?;
        self.error.write(w)?;
        self.measured.write(w)?;
        self.details.write(w)?;
        Ok(())
    }
}
impl Codec for RawRegion {
    fn read(r: &mut Reader<'_>) -> Result<Self, ()> {
        Ok(Self {
            name: <RawText>::read(r)?,
            exit_ip: <RawText>::read(r)?,
            country: <RawText>::read(r)?,
            status: <RawText>::read(r)?,
            sources: <Vec<RawText>>::read(r)?,
            detail: <RawText>::read(r)?,
            verified_at: <i64>::read(r)?,
        })
    }
    fn write(&self, w: &mut Writer) -> Result<(), ()> {
        self.name.write(w)?;
        self.exit_ip.write(w)?;
        self.country.write(w)?;
        self.status.write(w)?;
        self.sources.write(w)?;
        self.detail.write(w)?;
        self.verified_at.write(w)?;
        Ok(())
    }
}
impl Codec for RawHealth {
    fn read(r: &mut Reader<'_>) -> Result<Self, ()> {
        Ok(Self {
            name: <RawText>::read(r)?,
            state: <RawText>::read(r)?,
            consecutive_failures: <i64>::read(r)?,
            consecutive_successes: <i64>::read(r)?,
            latency_ms: <i64>::read(r)?,
            last_success_at: <i64>::read(r)?,
            cooldown_until: <i64>::read(r)?,
            updated_at: <i64>::read(r)?,
        })
    }
    fn write(&self, w: &mut Writer) -> Result<(), ()> {
        self.name.write(w)?;
        self.state.write(w)?;
        self.consecutive_failures.write(w)?;
        self.consecutive_successes.write(w)?;
        self.latency_ms.write(w)?;
        self.last_success_at.write(w)?;
        self.cooldown_until.write(w)?;
        self.updated_at.write(w)?;
        Ok(())
    }
}
impl Codec for RawSwitchEvent {
    fn read(r: &mut Reader<'_>) -> Result<Self, ()> {
        Ok(Self {
            at: <i64>::read(r)?,
            group: <RawText>::read(r)?,
            from_node: <RawText>::read(r)?,
            to_node: <RawText>::read(r)?,
            trigger: <RawText>::read(r)?,
            evidence: <RawText>::read(r)?,
        })
    }
    fn write(&self, w: &mut Writer) -> Result<(), ()> {
        self.at.write(w)?;
        self.group.write(w)?;
        self.from_node.write(w)?;
        self.to_node.write(w)?;
        self.trigger.write(w)?;
        self.evidence.write(w)?;
        Ok(())
    }
}
impl Codec for RawTrafficSample {
    fn read(r: &mut Reader<'_>) -> Result<Self, ()> {
        Ok(Self {
            has_id: <bool>::read(r)?,
            connection_key: <RawText>::read(r)?,
            app_key: <RawText>::read(r)?,
            app: <RawText>::read(r)?,
            target: <RawText>::read(r)?,
            network: <RawText>::read(r)?,
            chain: <RawText>::read(r)?,
            upload: <i64>::read(r)?,
            download: <i64>::read(r)?,
        })
    }
    fn write(&self, w: &mut Writer) -> Result<(), ()> {
        self.has_id.write(w)?;
        self.connection_key.write(w)?;
        self.app_key.write(w)?;
        self.app.write(w)?;
        self.target.write(w)?;
        self.network.write(w)?;
        self.chain.write(w)?;
        self.upload.write(w)?;
        self.download.write(w)?;
        Ok(())
    }
}
impl Codec for RawTrafficHistoryRow {
    fn read(r: &mut Reader<'_>) -> Result<Self, ()> {
        Ok(Self {
            id: <RawText>::read(r)?,
            app: <RawText>::read(r)?,
            app_key: <RawText>::read(r)?,
            target: <RawText>::read(r)?,
            network: <RawText>::read(r)?,
            chain: <RawText>::read(r)?,
            upload: <i64>::read(r)?,
            download: <i64>::read(r)?,
            connections: <i64>::read(r)?,
            first_seen: <i64>::read(r)?,
            last_seen: <i64>::read(r)?,
        })
    }
    fn write(&self, w: &mut Writer) -> Result<(), ()> {
        self.id.write(w)?;
        self.app.write(w)?;
        self.app_key.write(w)?;
        self.target.write(w)?;
        self.network.write(w)?;
        self.chain.write(w)?;
        self.upload.write(w)?;
        self.download.write(w)?;
        self.connections.write(w)?;
        self.first_seen.write(w)?;
        self.last_seen.write(w)?;
        Ok(())
    }
}
impl Codec for RawTrafficHistoryReport {
    fn read(r: &mut Reader<'_>) -> Result<Self, ()> {
        Ok(Self {
            period: <RawText>::read(r)?,
            date: <RawText>::read(r)?,
            group: <RawText>::read(r)?,
            timezone: <RawText>::read(r)?,
            upload_total: <i64>::read(r)?,
            download_total: <i64>::read(r)?,
            connection_count: <i64>::read(r)?,
            row_count: <i64>::read(r)?,
            offset: <i64>::read(r)?,
            limit: <i64>::read(r)?,
            has_more: <bool>::read(r)?,
            rows: <Vec<RawTrafficHistoryRow>>::read(r)?,
            sampling_error: <RawText>::read(r)?,
        })
    }
    fn write(&self, w: &mut Writer) -> Result<(), ()> {
        self.period.write(w)?;
        self.date.write(w)?;
        self.group.write(w)?;
        self.timezone.write(w)?;
        self.upload_total.write(w)?;
        self.download_total.write(w)?;
        self.connection_count.write(w)?;
        self.row_count.write(w)?;
        self.offset.write(w)?;
        self.limit.write(w)?;
        self.has_more.write(w)?;
        self.rows.write(w)?;
        self.sampling_error.write(w)?;
        Ok(())
    }
}
pub fn execute(store: &Store, op: u32, input: &[u8]) -> Reply {
    if input.len() > MAX_INPUT {
        return Reply::invalid();
    }
    let mut d = Reader { b: input, p: 0 };
    // This closure performs only decoding until each arm has called finish().
    let parsed: Result<Reply, ()> = (|| {
        Ok(match op {
            0 => {
                d.finish()?;
                Reply::ok(vec![])
            }
            1 => {
                let k = RawText::read(&mut d)?;
                let v = RawText::read(&mut d)?;
                d.finish()?;
                write_reply(store.set_value_raw(&k, &v))
            }
            2 => {
                let k = RawText::read(&mut d)?;
                d.finish()?;
                match store.get_value_raw(&k) {
                    Ok(v) => {
                        let encoded = (|| {
                            let mut w = Writer(vec![]);
                            v.is_some().write(&mut w)?;
                            if let Some(v) = v {
                                v.write(&mut w)?;
                            }
                            Ok::<_, ()>(w.0)
                        })();
                        match encoded {
                            Ok(b) => Reply::ok(b),
                            Err(_) => Reply {
                                category: 4,
                                sqlcode: 0,
                                outcome: 0,
                                payload: vec![],
                            },
                        }
                    }
                    Err(e) => error(e, false, vec![]),
                }
            }
            3 => {
                let at = i64::read(&mut d)?;
                let nodes = Vec::<RawNode>::read(&mut d)?;
                d.finish()?;
                write_reply(store.upsert_nodes_raw(&nodes, at))
            }
            4 => {
                let v = RawRegion::read(&mut d)?;
                d.finish()?;
                write_reply(store.save_region_raw(&v))
            }
            5 => {
                d.finish()?;
                read_reply(
                    store
                        .load_regions_raw()
                        .map(|v| v.into_values().collect::<Vec<_>>()),
                )
            }
            6 => {
                d.finish()?;
                write_reply(store.clear_regions_raw())
            }
            7 => {
                let v = RawHealth::read(&mut d)?;
                d.finish()?;
                write_reply(store.save_health_raw(&v))
            }
            8 => {
                d.finish()?;
                read_reply(
                    store
                        .load_health_raw()
                        .map(|v| v.into_values().collect::<Vec<_>>()),
                )
            }
            9 => {
                d.finish()?;
                write_reply(store.clear_health_raw())
            }
            10 => {
                let v = RawSwitchEvent::read(&mut d)?;
                d.finish()?;
                write_reply(store.append_switch_event_raw(&v))
            }
            11 => {
                let limit = i64::read(&mut d)?;
                d.finish()?;
                read_reply(store.recent_switch_events_raw(limit))
            }
            12 => {
                let v = RawProbeRow::read(&mut d)?;
                d.finish()?;
                write_reply(store.save_probe_row_raw(&v))
            }
            13 => {
                d.finish()?;
                read_reply(store.load_probe_rows_raw())
            }
            14 => {
                let session = RawText::read(&mut d)?;
                let bucket = RawText::read(&mut d)?;
                let now = i64::read(&mut d)?;
                let samples = Vec::<RawTrafficSample>::read(&mut d)?;
                d.finish()?;
                write_reply(store.record_traffic_raw(&session, &samples, &bucket, now))
            }
            15 => {
                let report = RawTrafficHistoryReport::read(&mut d)?;
                let key = RawText::read(&mut d)?;
                d.finish()?;
                let outcome = store.read_traffic_history_partial_raw(report, &key);
                match encoded(&outcome.report) {
                    Ok(payload) => match outcome.error {
                        Some(e) => error(e, false, payload),
                        None => Reply::ok(payload),
                    },
                    Err(_) => Reply {
                        category: 4,
                        sqlcode: 0,
                        outcome: 0,
                        payload: vec![],
                    },
                }
            }
            _ => return Err(()),
        })
    })();
    parsed.unwrap_or_else(|_| Reply::invalid())
}

#[cfg(test)]
mod tests {
    use super::*;
    fn call(s: &Store, op: u32, parts: &[Vec<u8>]) -> Reply {
        execute(s, op, &parts.concat())
    }
    fn bytes(s: &str) -> Vec<u8> {
        encoded(&RawText::from(s)).unwrap()
    }
    #[test]
    fn validates_before_sql_and_retains_exact_bytes() {
        let s = Store::open(":memory:").unwrap();
        let key = encoded(&RawText(vec![0, 0x80, 0xff])).unwrap();
        let value = bytes("v");
        let mut bad = [key.clone(), value.clone()].concat();
        bad.push(0);
        assert_eq!(execute(&s, 1, &bad).category, 1);
        assert_eq!(
            s.get_value_raw(&RawText(vec![0, 0x80, 0xff])).unwrap(),
            None
        );
        assert_eq!(call(&s, 1, &[key.clone(), value.clone()]).category, 0);
        let got = call(&s, 2, &[key]);
        assert_eq!(got.payload, [vec![1], value].concat());
        assert_eq!(execute(&s, 3, &[0; 7]).category, 1);
        assert_eq!(execute(&s, 99, &[]).category, 1);
        assert_eq!(execute(&s, 0, &[0]).category, 1);
        let mut count = 0i64.to_le_bytes().to_vec();
        count.extend_from_slice(&u64::MAX.to_le_bytes());
        assert_eq!(execute(&s, 3, &count).category, 1);
    }
    #[test]
    fn every_operation_routes_typed_records() {
        let s = Store::open(":memory:").unwrap();
        let b = RawText(vec![0x80, 0]);
        let n = RawNode {
            name: b.clone(),
            protocol: b.clone(),
            network: b.clone(),
            tls: true,
        };
        assert_eq!(
            call(
                &s,
                3,
                &[encoded(&i64::MAX).unwrap(), encoded(&vec![n]).unwrap()]
            )
            .category,
            0
        );
        let r = RawRegion {
            name: b.clone(),
            exit_ip: b.clone(),
            country: b.clone(),
            status: b.clone(),
            sources: vec![b.clone()],
            detail: b.clone(),
            verified_at: 1,
        };
        assert_eq!(execute(&s, 4, &encoded(&r).unwrap()).category, 0);
        assert_eq!(execute(&s, 5, &[]).payload, encoded(&vec![r]).unwrap());
        let h = RawHealth {
            name: b.clone(),
            state: b.clone(),
            consecutive_failures: 1,
            consecutive_successes: 2,
            latency_ms: 3,
            last_success_at: 4,
            cooldown_until: 5,
            updated_at: 6,
        };
        assert_eq!(execute(&s, 7, &encoded(&h).unwrap()).category, 0);
        assert_eq!(execute(&s, 8, &[]).payload, encoded(&vec![h]).unwrap());
        let e = RawSwitchEvent {
            at: 1,
            group: b.clone(),
            from_node: b.clone(),
            to_node: b.clone(),
            trigger: b.clone(),
            evidence: b.clone(),
        };
        assert_eq!(execute(&s, 10, &encoded(&e).unwrap()).category, 0);
        assert_eq!(
            execute(&s, 11, &encoded(&50i64).unwrap()).payload,
            encoded(&vec![e]).unwrap()
        );
        let p = RawProbeRow {
            path: b.clone(),
            fingerprint: b.clone(),
            delay: i64::MAX,
            error: b.clone(),
            measured: 1,
            details: b.clone(),
        };
        assert_eq!(execute(&s, 12, &encoded(&p).unwrap()).category, 0);
        assert_eq!(execute(&s, 13, &[]).payload, encoded(&vec![p]).unwrap());
        let sample = RawTrafficSample {
            has_id: true,
            connection_key: b.clone(),
            app_key: b.clone(),
            app: b.clone(),
            target: b.clone(),
            network: b.clone(),
            chain: b,
            upload: 9,
            download: 8,
        };
        let input = [
            bytes("s"),
            bytes("2026"),
            encoded(&1i64).unwrap(),
            encoded(&vec![sample]).unwrap(),
        ];
        assert_eq!(call(&s, 14, &input).category, 0);
        let q = RawTrafficHistoryReport {
            date: "2026".into(),
            group: "connection".into(),
            limit: 10,
            ..Default::default()
        };
        let reply = call(&s, 15, &[encoded(&q).unwrap(), bytes("")]);
        assert_eq!(reply.category, 0);
        let mut reader = Reader {
            b: &reply.payload,
            p: 0,
        };
        let report = RawTrafficHistoryReport::read(&mut reader).unwrap();
        reader.finish().unwrap();
        assert_eq!((report.upload_total, report.download_total), (9, 8));
        assert_eq!(execute(&s, 6, &[]).category, 0);
        assert_eq!(execute(&s, 9, &[]).category, 0);
        s.close().unwrap();
        let reply = call(&s, 15, &[encoded(&q).unwrap(), bytes("")]);
        assert_eq!(reply.category, 3);
        assert!(!reply.payload.is_empty());
    }
    #[test]
    fn raw_text_and_lists_reject_truncation_and_noncanonical_bools() {
        let s = Store::open(":memory:").unwrap();
        let valid = [bytes("k"), bytes("value")].concat();
        for i in 0..valid.len() {
            assert_eq!(execute(&s, 1, &valid[..i]).category, 1);
        }
        let mut d = Reader { b: &[2], p: 0 };
        assert!(bool::read(&mut d).is_err());
    }
}
