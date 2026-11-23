use super::*;
use std::{ffi::c_char, ffi::CStr, thread};
use tokio::sync::oneshot;

pub struct Session {
    stop: oneshot::Sender<()>,
    worker: thread::JoinHandle<()>,
}

// The caller owns both NUL-terminated strings and the writable output port
// for this call. A successful start transfers one session to the caller.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_host_start_v1(
    port: u16,
    key: *const c_char,
    token: *const c_char,
    bound: *mut u16,
) -> *mut Session {
    if port == 0 || key.is_null() || token.is_null() || bound.is_null() {
        return std::ptr::null_mut();
    }
    let start = || -> Option<(Session, u16)> {
        let key = unsafe { CStr::from_ptr(key) }.to_str().ok()?;
        let token = unsafe { CStr::from_ptr(token) }.to_str().ok()?;
        if [key, token]
            .iter()
            .any(|s| s.is_empty() || s.trim() != *s || HeaderValue::from_str(s).is_err())
        {
            return None;
        }
        let monitor = Monitor {
            reload_key: None,
            writes: false,
            resources: None,
            token: token.as_bytes().to_vec(),
            controller: Some(
                Controller {
                    port,
                    auth: HeaderValue::from_str(&format!("Bearer {key}")).ok()?,
                    node_timeout: 6000,
                    traffic: Mutex::default(),
                }
                .into(),
            ),
        };
        launch(monitor)
    };
    match start() {
        Some((session, port)) => {
            unsafe { bound.write(port) };
            Box::into_raw(Box::new(session))
        }
        None => std::ptr::null_mut(),
    }
}

// The owner takes the handle out of circulation before calling stop once.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_host_stop_v1(session: *mut Session) {
    if !session.is_null() {
        let session = unsafe { Box::from_raw(session) };
        let _ = session.stop.send(());
        let _ = session.worker.join();
    }
}

fn launch(monitor: Monitor) -> Option<(Session, u16)> {
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .ok()?;
    let listener = runtime
        .block_on(TcpListener::bind((std::net::Ipv4Addr::LOCALHOST, 0)))
        .ok()?;
    let bound = listener.local_addr().ok()?.port();
    let (stop, finished) = oneshot::channel();
    let name = if monitor.writes {
        "smartvpn-api"
    } else if monitor.resources.is_some() {
        "smartvpn-reads"
    } else {
        "smartvpn-monitor"
    };
    let worker = thread::Builder::new()
        .name(name.into())
        .spawn(move || {
            runtime.spawn(accept(listener, monitor));
            let _ = runtime.block_on(finished);
        })
        .ok()?;
    Some((Session { stop, worker }, bound))
}

pub fn start_reads(port: u16, key: &str, token: &str) -> Option<(Session, u16)> {
    start_api(port, key, token, false)
}
pub fn start_api(port: u16, key: &str, token: &str, writes: bool) -> Option<(Session, u16)> {
    if port == 0 || key.is_empty() || token.is_empty() {
        return None;
    }
    launch(Monitor {
        reload_key: None,
        writes,
        resources: Some((port, HeaderValue::from_str(&format!("Bearer {key}")).ok()?)),
        token: token.as_bytes().to_vec(),
        controller: None,
    })
}

#[no_mangle]
pub unsafe extern "C" fn smartvpn_host_reads_start_v1(
    port: u16,
    key: *const c_char,
    token: *const c_char,
    bound: *mut u16,
) -> *mut Session {
    start_session(port, key, token, bound, false)
}
#[no_mangle]
pub unsafe extern "C" fn smartvpn_host_api_start_v1(
    port: u16,
    key: *const c_char,
    token: *const c_char,
    bound: *mut u16,
) -> *mut Session {
    start_session(port, key, token, bound, true)
}
unsafe fn start_session(
    port: u16,
    key: *const c_char,
    token: *const c_char,
    bound: *mut u16,
    writes: bool,
) -> *mut Session {
    if key.is_null() || token.is_null() || bound.is_null() {
        return std::ptr::null_mut();
    }
    let (Ok(key), Ok(token)) = (CStr::from_ptr(key).to_str(), CStr::from_ptr(token).to_str())
    else {
        return std::ptr::null_mut();
    };
    if let Some((session, p)) = start_api(port, key, token, writes) {
        bound.write(p);
        Box::into_raw(Box::new(session))
    } else {
        std::ptr::null_mut()
    }
}
