#![cfg(unix)]
use smartvpn_engine::ffi::{
    smartvpn_engine_tun_running_v1 as running, smartvpn_engine_tun_start_v1 as start,
    smartvpn_engine_tun_stop_v1 as stop,
};
use smoltcp::{
    phy::ChecksumCapabilities,
    wire::{IpAddress, IpProtocol, IpRepr, UdpPacket, UdpRepr},
};
use std::{
    ffi::{c_char, CStr, CString},
    net::UdpSocket,
    os::{fd::AsRawFd, unix::net::UnixDatagram},
    sync::atomic::{AtomicU16, AtomicUsize, Ordering::SeqCst},
    time::Duration,
};

static DEST: AtomicU16 = AtomicU16::new(0);
static PROTECTED: AtomicUsize = AtomicUsize::new(0);

extern "C" fn protect(fd: i32) -> i32 {
    if unsafe { libc::fcntl(fd, libc::F_GETFD) } < 0 {
        return 0;
    }
    PROTECTED.fetch_add(1, SeqCst);
    1
}

extern "C" fn owner(
    proto: u8,
    src: *const c_char,
    sport: u16,
    dst: *const c_char,
    dport: u16,
    out: *mut c_char,
    capacity: usize,
) -> i32 {
    if proto != 17
        || sport != 42000
        || dport != DEST.load(SeqCst)
        || capacity < 3
        || unsafe {
            CStr::from_ptr(src).to_bytes() != b"10.0.0.2"
                || CStr::from_ptr(dst).to_bytes() != b"127.0.0.1"
        }
    {
        return -1;
    }
    unsafe {
        std::ptr::copy_nonoverlapping(b"app".as_ptr(), out.cast(), 3);
    }
    3
}

#[test]
fn ffi_routes_udp_and_joins_worker() {
    let yaml = CString::new("mixed-port: 7890\nrules: ['PROCESS-NAME,app,DIRECT', 'MATCH,REJECT']")
        .unwrap();
    assert!(unsafe { start(-1, yaml.as_ptr(), Some(protect), Some(owner)) } <= 0);
    let (peer, engine) = UnixDatagram::pair().unwrap();
    let handle = unsafe {
        start(
            engine.as_raw_fd(),
            yaml.as_ptr(),
            Some(protect),
            Some(owner),
        )
    };
    assert!(handle > 0);
    assert_eq!(running(handle), 1);
    drop(engine);
    let echo = UdpSocket::bind("127.0.0.1:0").unwrap();
    let port = echo.local_addr().unwrap().port();
    DEST.store(port, SeqCst);
    let timeout = Some(Duration::from_secs(3));
    peer.set_read_timeout(timeout).unwrap();
    echo.set_read_timeout(timeout).unwrap();
    let ip = IpRepr::new(
        IpAddress::v4(10, 0, 0, 2),
        IpAddress::v4(127, 0, 0, 1),
        IpProtocol::Udp,
        12,
        64,
    );
    let caps = ChecksumCapabilities::default();
    let mut packet = [0; 32];
    ip.emit(&mut packet, &caps);
    UdpRepr {
        src_port: 42000,
        dst_port: port,
    }
    .emit(
        &mut UdpPacket::new_unchecked(&mut packet[20..]),
        &ip.src_addr(),
        &ip.dst_addr(),
        4,
        |b| b.copy_from_slice(b"ping"),
        &caps,
    );
    peer.send(&packet).unwrap();
    let mut buf = [0; 64];
    let (n, addr) = echo.recv_from(&mut buf).unwrap();
    assert_eq!(&buf[..n], b"ping");
    assert_eq!(PROTECTED.load(SeqCst), 1);
    echo.send_to(b"pong", addr).unwrap();
    let n = peer.recv(&mut buf).unwrap();
    assert_eq!(&buf[28..n], b"pong");
    assert_eq!(stop(handle), 0);
    assert_eq!(running(handle), 0);
    assert_eq!(stop(handle), -1);
}
