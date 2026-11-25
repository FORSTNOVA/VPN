//! Proxy transports for the standalone SmartVPN data plane.
pub mod chain;
pub mod dialer;
pub mod ffi;
pub mod http_connect;
pub mod protocol;
pub mod socks5;
pub mod transport;
pub mod dns;
pub mod inbound;
pub mod router;
pub mod domestic;
pub mod rule;
pub mod config_bridge;
pub mod dns_server;
#[cfg(windows)]
pub mod process_win;
mod proxy_group;
pub mod traffic;
pub mod tun;

#[cfg(target_os = "android")]
fn android_network_log(detail: &str) {
    use std::ffi::{c_char, c_int, CString};
    #[link(name = "log")]
    extern "C" {
        fn __android_log_write(priority: c_int, tag: *const c_char, text: *const c_char) -> c_int;
    }
    let message = CString::new(detail.replace('\0', " ")).unwrap();
    unsafe { __android_log_write(4, c"SmartVPN-Hop".as_ptr(), message.as_ptr()); }
}
