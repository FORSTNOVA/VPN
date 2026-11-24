use std::{
    ffi::{c_char, CStr},
    net::TcpStream,
};

/// Negotiate one TCP hop on a connected socket. Call again on the same socket
/// for the next hop. Protocol 1 is HTTP CONNECT and 2 is SOCKS5.
/// Returns 0 on success, -1 for invalid arguments, -2 on transport failure.
///
/// # Safety
/// Supply a live socket owned by the caller with exclusive I/O access and
/// appropriate timeouts. String pointers must be valid NUL-terminated strings.
/// Pass two null credential pointers for anonymous access. Close the socket
/// following a handshake failure.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_engine_connect_v1(
    socket: usize,
    protocol: u32,
    host: *const c_char,
    port: u16,
    username: *const c_char,
    password: *const c_char,
) -> i32 {
    if host.is_null() || port == 0 || username.is_null() != password.is_null() {
        return -1;
    }
    let Ok(host) = CStr::from_ptr(host).to_str() else {
        return -1;
    };
    let credentials = if username.is_null() {
        None
    } else {
        Some((
            CStr::from_ptr(username).to_bytes(),
            CStr::from_ptr(password).to_bytes(),
        ))
    };
    let transport = match protocol {
        1 => crate::chain::Transport::Http(credentials),
        2 => crate::chain::Transport::Socks5(credentials),
        _ => return -1,
    };
    #[cfg(windows)]
    let duplicate = {
        use std::os::windows::io::BorrowedSocket;
        if socket == usize::MAX {
            return -1;
        }
        BorrowedSocket::borrow_raw(socket as _).try_clone_to_owned()
    };
    #[cfg(unix)]
    let duplicate = {
        use std::os::fd::BorrowedFd;
        let Ok(fd) = i32::try_from(socket) else {
            return -1;
        };
        BorrowedFd::borrow_raw(fd).try_clone_to_owned()
    };
    let Ok(handle) = duplicate else { return -2 };
    match crate::chain::connect(TcpStream::from(handle), transport, &[], (host, port)) {
        Ok(_) => 0,
        Err(_) => -2,
    }
}

/// Validate the Clash configuration used by TunStack. Returns 0 on success,
/// -1 for null/invalid UTF-8 and -2 for a configuration error.
/// # Safety
/// yaml must be null or point to a readable NUL-terminated string.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_engine_tun_validate_yaml_v1(yaml: *const c_char) -> i32 {
    if yaml.is_null() {
        return -1;
    }
    let Ok(text) = CStr::from_ptr(yaml).to_str() else {
        return -1;
    };
    match crate::config_bridge::build_router_from_clash_yaml(text, None) {
        Ok(_) => 0,
        Err(err) => {
            eprintln!("TUN configuration: {err}");
            -2
        }
    }
}
