//! Socket-free HTTP compatibility core. No handler effects, listeners or user state.
//! Route matching intentionally covers exact canonical API paths only; Go's mux
//! still owns redirects, escaped-path normalization and actual business dispatch.

pub const MAX_INPUT: usize = 8 << 20;

pub fn authorized(header: &[u8], token: &[u8]) -> bool {
    header.strip_prefix(b"Bearer ") == Some(token)
}
pub fn identity_json(connected: bool) -> &'static [u8] {
    if connected {
        b"{\"app\":\"SmartVPN\",\"connected\":true}\n"
    } else {
        b"{\"app\":\"SmartVPN\",\"connected\":false}\n"
    }
}

/// Route ID = index + 1. Windows-only driver route is the final entry.
pub const ROUTES: &[(&str, &str)] = &[
    ("GET", "/api/identify"),
    ("GET", "/api/state"),
    ("GET", "/api/subscription/policy"),
    ("PUT", "/api/settings"),
    ("POST", "/api/connect"),
    ("POST", "/api/disconnect"),
    ("POST", "/api/proxy/reapply"),
    ("GET", "/api/nodes"),
    ("GET", "/api/nodes/latency"),
    ("POST", "/api/nodes/fetch"),
    ("GET", "/api/diagnostics/subscription"),
    ("POST", "/api/nodes/refresh"),
    ("PUT", "/api/nodes/select"),
    ("POST", "/api/shutdown"),
    ("POST", "/api/direct/refresh"),
    ("GET", "/api/latency"),
    ("POST", "/api/diagnose"),
    ("POST", "/api/speedtest"),
    ("GET", "/api/path-checks"),
    ("PUT", "/api/path-checks"),
    ("POST", "/api/path-checks/run"),
    ("POST", "/api/path-checks/recovery/cancel"),
    ("GET", "/api/traffic"),
    ("GET", "/api/traffic/history"),
    ("GET", "/api/nodes/manual"),
    ("PUT", "/api/nodes/chain/profile"),
    ("GET", "/api/nodes/probes"),
    ("POST", "/api/nodes/chain/probe"),
    ("PUT", "/api/nodes/chain"),
    ("POST", "/api/nodes/manual"),
    ("DELETE", "/api/nodes/manual"),
    ("GET", "/api/subscriptions"),
    ("POST", "/api/subscriptions/activate"),
    ("POST", "/api/subscriptions/add"),
    ("POST", "/api/subscriptions/update"),
    ("POST", "/api/subscriptions/merge"),
    ("POST", "/api/subscriptions/refresh"),
    ("POST", "/api/subscriptions/quota"),
    ("POST", "/api/subscriptions/auto-merge"),
    ("PUT", "/api/subscriptions"),
    ("DELETE", "/api/subscriptions"),
    ("GET", "/api/regions"),
    ("POST", "/api/regions/verify"),
    ("POST", "/api/region/lock"),
    ("GET", "/api/events"),
    ("PUT", "/api/health/params"),
    ("POST", "/api/health/patrol"),
    ("GET", "/api/tun"),
    ("POST", "/api/tun/check"),
    ("POST", "/api/tun/wintun"),
];

#[derive(Debug, PartialEq, Eq)]
pub enum Dispatch {
    Route(usize),
    MethodNotAllowed,
    NotFound,
}
pub fn dispatch(method: &[u8], path: &[u8], windows: bool) -> Dispatch {
    let mut known_path = false;
    for (index, (allowed, route)) in ROUTES.iter().enumerate() {
        if index + 1 == ROUTES.len() && !windows {
            continue;
        }
        if path != route.as_bytes() {
            continue;
        }
        known_path = true;
        if method == allowed.as_bytes() || (method == b"HEAD" && *allowed == "GET") {
            return Dispatch::Route(index + 1);
        }
    }
    if known_path {
        Dispatch::MethodNotAllowed
    } else {
        Dispatch::NotFound
    }
}

/// Transport-independent request outcome. Delegation is explicit: this module
/// cannot connect VPNs, mutate settings or pretend those business handlers ran.
#[derive(Debug, PartialEq, Eq)]
pub enum ResponsePlan {
    Unauthorized,
    NotFound,
    MethodNotAllowed,
    Identity(&'static [u8]),
    Delegate { route_id: usize },
}

/// A request-time snapshot supplied by the owner of app/kernel state. This core
/// does not cache a second potentially-stale copy of the running VPN state.
pub struct RequestContext<'a> {
    pub token: &'a [u8],
    pub connected: bool,
    pub windows: bool,
}

pub fn plan_request(
    context: &RequestContext<'_>,
    method: &[u8],
    path: &[u8],
    header: &[u8],
) -> ResponsePlan {
    let route_id = match dispatch(method, path, context.windows) {
        Dispatch::NotFound => return ResponsePlan::NotFound,
        Dispatch::MethodNotAllowed => return ResponsePlan::MethodNotAllowed,
        Dispatch::Route(id) => id,
    };
    if route_id == 1 {
        return ResponsePlan::Identity(identity_json(context.connected));
    }
    if !authorized(header, context.token) {
        return ResponsePlan::Unauthorized;
    }
    ResponsePlan::Delegate { route_id }
}

unsafe fn input<'a>(ptr: *const u8, len: usize) -> Option<&'a [u8]> {
    if len > MAX_INPUT || (len != 0 && ptr.is_null()) {
        return None;
    }
    if len == 0 {
        Some(&[])
    } else {
        Some(std::slice::from_raw_parts(ptr, len))
    }
}

/// Returns 1 authorized, 0 rejected, -1 invalid input. Bytes need not be UTF-8.
/// # Safety
/// Non-null pointers must reference readable ranges for the provided lengths.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_http_authorized_v1(
    header: *const u8,
    header_len: usize,
    token: *const u8,
    token_len: usize,
) -> i32 {
    let (Some(header), Some(token)) = (input(header, header_len), input(token, token_len)) else {
        return -1;
    };
    i32::from(authorized(header, token))
}

/// Returns 0 success, -1 invalid arguments, -2 insufficient capacity.
/// # Safety
/// written must be writable; output must reference capacity writable bytes and
/// not overlap written. No pointers retained; output unchanged on failure.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_http_identity_v1(
    connected: u32,
    output: *mut u8,
    capacity: usize,
    written: *mut usize,
) -> i32 {
    if written.is_null() {
        return -1;
    }
    *written = 0;
    if connected > 1 || (capacity != 0 && output.is_null()) {
        return -1;
    }
    let body = identity_json(connected == 1);
    *written = body.len();
    if capacity < body.len() {
        return -2;
    }
    std::ptr::copy_nonoverlapping(body.as_ptr(), output, body.len());
    0
}

/// Returns positive route ID, 0 absent path, -2 wrong method, -1 invalid input.
/// # Safety
/// Each input pointer must reference its readable length; no retention.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_http_dispatch_v1(
    method: *const u8,
    method_len: usize,
    path: *const u8,
    path_len: usize,
    windows: u32,
) -> i32 {
    if windows > 1 {
        return -1;
    }
    let (Some(method), Some(path)) = (input(method, method_len), input(path, path_len)) else {
        return -1;
    };
    match dispatch(method, path, windows == 1) {
        Dispatch::Route(id) => id as i32,
        Dispatch::NotFound => 0,
        Dispatch::MethodNotAllowed => -2,
    }
}

/// Chooses from a caller-owned app snapshot. Nonpositive delays are unavailable;
/// a reachable preferred node wins, otherwise first minimum positive delay wins.
pub fn choose_reachable(delays: &[i64], preferred: Option<usize>) -> Option<usize> {
    let mut best = None;
    for (i, &delay) in delays.iter().enumerate() {
        if delay <= 0 {
            continue;
        }
        if preferred == Some(i) {
            return Some(i);
        }
        if best.is_none_or(|b| delay < delays[b]) {
            best = Some(i);
        }
    }
    best
}

/// Returns chosen index, -1 none, -2 invalid. preferred=-1 means no preference.
/// # Safety
/// delays must be aligned and readable for count i64 values (NULL only if empty).
#[no_mangle]
pub unsafe extern "C" fn smartvpn_choose_reachable_v1(
    delays: *const i64,
    count: usize,
    preferred: i64,
) -> i64 {
    if count > 1_000_000
        || (count != 0
            && (delays.is_null() || !(delays as usize).is_multiple_of(std::mem::align_of::<i64>())))
        || preferred < -1
        || (preferred >= 0 && preferred as u64 >= count as u64)
    {
        return -2;
    }
    let values = if count == 0 {
        &[]
    } else {
        std::slice::from_raw_parts(delays, count)
    };
    choose_reachable(
        values,
        if preferred < 0 {
            None
        } else {
            Some(preferred as usize)
        },
    )
    .map_or(-1, |i| i as i64)
}

#[no_mangle]
pub extern "C" fn smartvpn_read_api_version_v1() -> u32 { 1 }

#[no_mangle]
pub extern "C" fn smartvpn_write_api_version_v1()->u32{1}

#[cfg(windows)]
#[no_mangle]
pub extern "C" fn smartvpn_windows_kernel_version_v1() -> u32 {
    1
}
