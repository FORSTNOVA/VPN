use super::TunDevice;
use crate::{dialer::BoxFuture, router::ProcessResolver};
use std::{
    ffi::{c_char, CString},
    fs::File,
    io::{self, Read, Write},
    net::SocketAddr,
    os::fd::{AsRawFd, FromRawFd},
};
use tokio::io::unix::AsyncFd;

pub struct AndroidTun(AsyncFd<File>);

impl AndroidTun {
    pub fn new(tun_fd: i32) -> io::Result<Self> {
        open_tun_fd(tun_fd)
    }
}

/// Duplicate the VpnService descriptor. Nonblocking mode is shared with the
/// original open file description. Only the engine should perform packet I/O.
pub fn open_tun_fd(tun_fd: i32) -> io::Result<AndroidTun> {
    let fd = unsafe { libc::fcntl(tun_fd, libc::F_DUPFD_CLOEXEC, 0) };
    if fd < 0 {
        return Err(io::Error::last_os_error());
    }
    let file = unsafe { File::from_raw_fd(fd) };
    let flags = unsafe { libc::fcntl(file.as_raw_fd(), libc::F_GETFL) };
    if flags < 0 || unsafe { libc::fcntl(fd, libc::F_SETFL, flags | libc::O_NONBLOCK) } < 0 {
        return Err(io::Error::last_os_error());
    }
    // File owns the duplicated descriptor for the entire registration.
    Ok(AndroidTun(unsafe { AsyncFd::register(file) }?))
}
impl TunDevice for AndroidTun {
    fn recv_packet(&mut self) -> BoxFuture<'_, io::Result<Vec<u8>>> {
        Box::pin(async {
            let mut packet = vec![0; 65535];
            let n = self
                .0
                .async_io(tokio::io::Interest::READABLE, |mut f| f.read(&mut packet))
                .await?;
            if n == 0 {
                return Err(io::ErrorKind::UnexpectedEof.into());
            }
            packet.truncate(n);
            Ok(packet)
        })
    }
    fn send_packet<'a>(&'a self, pkt: &'a [u8]) -> BoxFuture<'a, io::Result<()>> {
        Box::pin(async {
            let n = self
                .0
                .async_io(tokio::io::Interest::WRITABLE, |mut f| f.write(pkt))
                .await?;
            if n != pkt.len() {
                return Err(io::Error::new(
                    io::ErrorKind::WriteZero,
                    "short TUN packet write",
                ));
            }
            Ok(())
        })
    }
}
/// The callback receives NUL-terminated source and destination IP strings and separate ports,
/// IP protocol 6/17, and a caller-owned UTF-8 output buffer. Return the byte
/// count excluding NUL, zero for unknown owner, or a negative value on error.
pub use crate::ffi::OwnerCallback;
pub struct CallbackProcessResolver(OwnerCallback);
impl CallbackProcessResolver {
    /// # Safety
    /// Callback must stay loaded, be thread-safe, respect the output capacity,
    /// and retain none of its arguments.
    pub unsafe fn new(callback: OwnerCallback) -> Self {
        Self(callback)
    }
}
impl ProcessResolver for CallbackProcessResolver {
    fn resolve(&self, src: SocketAddr, dst: SocketAddr, udp: bool) -> Option<String> {
        self.try_resolve(src, dst, udp)
            .expect("Android owner lookup failed")
    }
    fn try_resolve(
        &self,
        src: SocketAddr,
        dst: SocketAddr,
        udp: bool,
    ) -> io::Result<Option<String>> {
        let src_ip = CString::new(src.ip().to_string()).unwrap();
        let dst_ip = CString::new(dst.ip().to_string()).unwrap();
        let mut out = [0; 512];
        let len = (self.0)(
            if udp { 17 } else { 6 },
            src_ip.as_ptr(),
            src.port(),
            dst_ip.as_ptr(),
            dst.port(),
            out.as_mut_ptr().cast::<c_char>(),
            out.len(),
        );
        if len == 0 {
            return Ok(None);
        }
        let bytes = out
            .get(..len as usize)
            .ok_or_else(|| io::Error::other(format!("owner callback returned {len}")))?;
        Ok(Some(
            std::str::from_utf8(bytes)
                .map_err(io::Error::other)?
                .to_owned(),
        ))
    }
}
