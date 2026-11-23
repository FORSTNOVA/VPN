//! Deliberately not HTTP: bounded binary stdin/stdout adapter for offline replay.
//! Frame = u32 LE payload length; payload = id:u64, op:u8. op1 carries three
//! u32-length byte strings (method,path,authorization); op2 carries revision:u64
//! and an error byte string; op3 snapshot; op4 shutdown. Response payload is
//! status:u16, revision:u64 and a u32-length body. No untrusted allocations occur
//! before the outer bound check. Truncated frames are fatal, never resynchronized.
use crate::{Admission, Host, HostError, MemoryResources, Request, Response, MAX_FRAME};
use std::io::{self, Read, Write};
fn field<'a>(data: &mut &'a [u8]) -> io::Result<&'a [u8]> {
    let n = u32::from_le_bytes(take(data, 4)?.try_into().unwrap()) as usize;
    take(data, n)
}
fn take<'a>(data: &mut &'a [u8], n: usize) -> io::Result<&'a [u8]> {
    if n > data.len() {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "truncated payload",
        ));
    }
    let (a, b) = data.split_at(n);
    *data = b;
    Ok(a)
}
pub(crate) fn decode(mut data: &[u8]) -> io::Result<(u64, Option<Request>)> {
    let id = u64::from_le_bytes(take(&mut data, 8)?.try_into().unwrap());
    let op = take(&mut data, 1)?[0];
    let q = match op {
        1 => Some(Request::Http {
            method: field(&mut data)?.to_vec(),
            path: field(&mut data)?.to_vec(),
            authorization: field(&mut data)?.to_vec(),
        }),
        2 => {
            let expected_revision = u64::from_le_bytes(take(&mut data, 8)?.try_into().unwrap());
            Some(Request::SetSamplingError {
                expected_revision,
                value: field(&mut data)?.to_vec(),
            })
        }
        3 => Some(Request::Snapshot),
        4 => None,
        _ => {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "unknown operation",
            ))
        }
    };
    if !data.is_empty() {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "trailing payload",
        ));
    }
    Ok((id, q))
}
pub fn encode_request(id: u64, q: Option<&Request>) -> Vec<u8> {
    fn field(b: &mut Vec<u8>, v: &[u8]) {
        b.extend_from_slice(&(v.len() as u32).to_le_bytes());
        b.extend_from_slice(v);
    }
    let mut b = id.to_le_bytes().to_vec();
    match q {
        Some(Request::Http {
            method,
            path,
            authorization,
        }) => {
            b.push(1);
            for v in [method, path, authorization] {
                field(&mut b, v);
            }
        }
        Some(Request::SetSamplingError {
            expected_revision,
            value,
        }) => {
            b.push(2);
            b.extend_from_slice(&expected_revision.to_le_bytes());
            field(&mut b, value);
        }
        Some(Request::Snapshot) => b.push(3),
        None => b.push(4),
    }
    let mut framed = (b.len() as u32).to_le_bytes().to_vec();
    framed.extend(b);
    framed
}
pub(crate) fn write_response(w: &mut impl Write, r: Response) -> io::Result<()> {
    let mut b = r.status.to_le_bytes().to_vec();
    b.extend_from_slice(&r.revision.to_le_bytes());
    b.extend_from_slice(&(r.body.len() as u32).to_le_bytes());
    b.extend(r.body);
    w.write_all(&(b.len() as u32).to_le_bytes())?;
    w.write_all(&b)?;
    w.flush()
}
pub fn run(reader: &mut impl Read, writer: &mut impl Write) -> io::Result<()> {
    let mut host = Host::new(
        1,
        b"offline-synthetic".to_vec(),
        true,
        MemoryResources::default(),
    )
    .map_err(|_| io::Error::other("host initialization"))?;
    let result = (|| {
        loop {
            let mut len = [0; 4];
            match reader.read(&mut len[..1])? {
                0 => break,
                1 => reader.read_exact(&mut len[1..])?,
                _ => unreachable!(),
            }
            let n = u32::from_le_bytes(len) as usize;
            if n > MAX_FRAME {
                return Err(io::Error::new(io::ErrorKind::InvalidData, "frame limit"));
            }
            let mut frame = vec![0; n];
            reader.read_exact(&mut frame)?;
            let (id, q) = decode(&frame)?;
            let result = if let Some(q) = q {
                match host.admit(id, q) {
                    Ok(Admission::Execute(t)) => host.execute(t),
                    Ok(Admission::Replay(r)) => Ok(r),
                    Err(e) => Err(e),
                }
            } else {
                host.begin_shutdown();
                Ok(Response {
                    status: 200,
                    revision: 0,
                    body: b"closed\n".to_vec(),
                })
            };
            let response = result.unwrap_or_else(|e| Response {
                status: if matches!(
                    e,
                    HostError::Closing | HostError::Closed | HostError::StoreUncertain
                ) {
                    503
                } else {
                    409
                },
                revision: 0,
                body: format!("{e:?}\n").into_bytes(),
            });
            write_response(writer, response)?;
        }
        Ok(())
    })();
    host.begin_shutdown();
    result
}

/// Disk mode uses only a caller-owned dedicated directory, never the app DB.
pub fn run_disk(
    reader: &mut impl Read,
    writer: &mut impl Write,
    directory: &std::path::Path,
) -> io::Result<()> {
    use crate::durable::{DiskJournal, DurableHost};
    let journal = DiskJournal::open(directory).map_err(|e| io::Error::other(format!("{e:?}")))?;
    let mut host = DurableHost::open(journal, 1, b"offline-synthetic".to_vec(), true)
        .map_err(|e| io::Error::other(format!("{e:?}")))?;
    let result = (|| {
        loop {
            let mut len = [0; 4];
            match reader.read(&mut len[..1])? {
                0 => break,
                1 => reader.read_exact(&mut len[1..])?,
                _ => unreachable!(),
            }
            let n = u32::from_le_bytes(len) as usize;
            if n > MAX_FRAME {
                return Err(io::Error::new(io::ErrorKind::InvalidData, "frame limit"));
            }
            let mut frame = vec![0; n];
            reader.read_exact(&mut frame)?;
            let (id, q) = decode(&frame)?;
            if let Some(q) = q {
                let response = host
                    .request(id, q)
                    .map_err(|e| io::Error::other(format!("{e:?}")))?;
                write_response(writer, response)?;
            } else {
                host.close()
                    .map_err(|e| io::Error::other(format!("{e:?}")))?;
                write_response(
                    writer,
                    Response {
                        status: 200,
                        revision: 0,
                        body: b"closed\n".to_vec(),
                    },
                )?;
                break;
            }
        }
        Ok(())
    })();
    let closed = host.close().map_err(|e| io::Error::other(format!("{e:?}")));
    result.and(closed)
}
