//! HTTP composition only. Transport normalization stays with the existing host.
//! No handler is retried: planning finishes before any application effect.
use crate::http_core::{authorized, dispatch, Dispatch};
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Plan {
    pub route: u32,
    pub status: u16,
    pub json: bool,
}
pub fn plan(method: &[u8], path: &[u8], header: &[u8], token: &[u8], windows: bool) -> Plan {
    let route = match dispatch(method, path, windows) {
        Dispatch::Route(id) => id as u32,
        Dispatch::NotFound => {
            return Plan {
                route: 0,
                status: 404,
                json: false,
            }
        }
        Dispatch::MethodNotAllowed => {
            return Plan {
                route: 0,
                status: 405,
                json: false,
            }
        }
    };
    authorization(route, route == 1, header, token)
}
pub fn authorization(route: u32, public: bool, header: &[u8], token: &[u8]) -> Plan {
    if !public && !authorized(header, token) {
        Plan {
            route: 0,
            status: 401,
            json: false,
        }
    } else {
        Plan {
            route,
            status: 0,
            json: !public,
        }
    }
}
pub trait Effects {
    type Error;
    fn reject(&mut self, status: u16) -> Result<(), Self::Error>;
    fn json_header(&mut self) -> Result<(), Self::Error>;
    fn invoke(&mut self, route: u32) -> Result<(), Self::Error>;
}
pub fn execute<E: Effects>(p: Plan, e: &mut E) -> Result<(), E::Error> {
    if p.status != 0 {
        return e.reject(p.status);
    }
    if p.json {
        e.json_header()?;
    }
    e.invoke(p.route)
}
const MAX: usize = 8 << 20;
struct Reader<'a> {
    b: &'a [u8],
    p: usize,
}
impl<'a> Reader<'a> {
    fn bytes(&mut self) -> Option<&'a [u8]> {
        let end = self.p.checked_add(4)?;
        let n = u32::from_le_bytes(self.b.get(self.p..end)?.try_into().ok()?) as usize;
        self.p = end;
        let end = self.p.checked_add(n)?;
        let b = self.b.get(self.p..end)?;
        self.p = end;
        Some(b)
    }
}
/// Pure, fixed-size response (route u32, status u16, JSON u8). No effect or pointer retained.
/// # Safety
/// Input/output must reference readable/writable ranges of their declared lengths.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_http_composition_v1(
    input: *const u8,
    len: usize,
    output: *mut u8,
    cap: usize,
) -> i32 {
    if input.is_null() || output.is_null() || len > MAX || cap < 7 {
        return -1;
    }
    let b = std::slice::from_raw_parts(input, len);
    let mut r = Reader { b, p: 0 };
    let result = (|| {
        let method = r.bytes()?;
        let path = r.bytes()?;
        let header = r.bytes()?;
        let token = r.bytes()?;
        let flags = *b.get(r.p)?;
        r.p += 1;
        if flags > 3 || r.p != len {
            return None;
        }
        Some(if flags & 2 != 0 {
            authorization(0, false, header, token)
        } else {
            plan(method, path, header, token, flags & 1 != 0)
        })
    })();
    let Some(p) = result else { return -1 };
    let mut out = [0u8; 7];
    out[..4].copy_from_slice(&p.route.to_le_bytes());
    out[4..6].copy_from_slice(&p.status.to_le_bytes());
    out[6] = u8::from(p.json);
    std::ptr::copy_nonoverlapping(out.as_ptr(), output, 7);
    0
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn routes_auth_and_execution() {
        for (windows, count) in [(false, 49), (true, 50)] {
            for (i, (method, path)) in crate::http_core::ROUTES.iter().take(count).enumerate() {
                let p = plan(
                    method.as_bytes(),
                    path.as_bytes(),
                    b"Bearer token",
                    b"token",
                    windows,
                );
                assert_eq!(p.route, i as u32 + 1);
                assert_eq!(p.json, i != 0);
                assert_eq!(
                    plan(
                        method.as_bytes(),
                        path.as_bytes(),
                        b"bad",
                        b"token",
                        windows
                    )
                    .status,
                    if i == 0 { 0 } else { 401 }
                );
            }
        }
    }
    #[test]
    fn errors_precede_authorization() {
        assert_eq!(plan(b"POST", b"/api/state", b"bad", b"", true).status, 405);
        assert_eq!(plan(b"GET", b"/absent", b"bad", b"", true).status, 404);
        assert_eq!(plan(b"HEAD", b"/api/state", b"Bearer ", b"", true).route, 2);
    }
    #[test]
    fn invocation_failure_not_replayed() {
        struct E(usize);
        impl Effects for E {
            type Error = ();
            fn reject(&mut self, _: u16) -> Result<(), ()> {
                Ok(())
            }
            fn json_header(&mut self) -> Result<(), ()> {
                Ok(())
            }
            fn invoke(&mut self, _: u32) -> Result<(), ()> {
                self.0 += 1;
                Err(())
            }
        }
        let mut e = E(0);
        assert!(execute(
            Plan {
                route: 2,
                status: 0,
                json: true
            },
            &mut e
        )
        .is_err());
        assert_eq!(e.0, 1);
    }
}
