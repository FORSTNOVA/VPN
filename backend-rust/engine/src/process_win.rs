use crate::router::ProcessResolver;
use std::{
    ffi::OsString,
    io,
    mem::size_of,
    net::SocketAddr,
    os::windows::{
        ffi::OsStringExt,
        io::{AsRawHandle, FromRawHandle, OwnedHandle},
    },
    path::Path,
};
use windows_sys::Win32::{
    Foundation::{ERROR_INSUFFICIENT_BUFFER, NO_ERROR},
    NetworkManagement::IpHelper::{
        GetExtendedTcpTable, MIB_TCP6ROW_OWNER_PID, MIB_TCPROW_OWNER_PID, TCP_TABLE_OWNER_PID_ALL,
    },
    Networking::WinSock::{AF_INET, AF_INET6},
    System::Threading::{
        OpenProcess, QueryFullProcessImageNameW, PROCESS_QUERY_LIMITED_INFORMATION,
    },
};

pub struct WindowsProcessResolver;

fn owner(src: SocketAddr) -> io::Result<Option<u32>> {
    let family = if src.is_ipv4() { AF_INET } else { AF_INET6 };
    let mut words: Vec<u32> = Vec::new();
    let mut bytes = 0;
    loop {
        let buffer = if words.is_empty() {
            std::ptr::null_mut()
        } else {
            words.as_mut_ptr().cast()
        };
        let status = unsafe {
            GetExtendedTcpTable(
                buffer,
                &mut bytes,
                0,
                family.into(),
                TCP_TABLE_OWNER_PID_ALL,
                0,
            )
        };
        match status {
            NO_ERROR => break,
            ERROR_INSUFFICIENT_BUFFER => words.resize((bytes as usize).div_ceil(4), 0),
            code => return Err(io::Error::from_raw_os_error(code as i32)),
        }
    }
    // Both Windows table layouts start with a DWORD count followed by DWORD-aligned rows.
    let count = words[0] as usize;
    macro_rules! rows {
        ($row:ty) => {{
            let data = &words[1..];
            if count > std::mem::size_of_val(data) / size_of::<$row>() {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidData,
                    "truncated Windows TCP table",
                ));
            }
            unsafe { std::slice::from_raw_parts(data.as_ptr().cast::<$row>(), count) }
        }};
    }
    Ok(match src {
        SocketAddr::V4(a) => rows!(MIB_TCPROW_OWNER_PID)
            .iter()
            .find(|r| {
                r.dwLocalAddr.to_ne_bytes() == a.ip().octets()
                    && u16::from_be(r.dwLocalPort as u16) == a.port()
            })
            .map(|r| r.dwOwningPid),
        SocketAddr::V6(a) => rows!(MIB_TCP6ROW_OWNER_PID)
            .iter()
            .find(|r| {
                r.ucLocalAddr == a.ip().octets()
                    && r.dwLocalScopeId == a.scope_id()
                    && u16::from_be(r.dwLocalPort as u16) == a.port()
            })
            .map(|r| r.dwOwningPid),
    })
}

fn process_name(src: SocketAddr) -> io::Result<Option<String>> {
    let Some(pid) = owner(src)? else {
        return Ok(None);
    };
    let handle = unsafe { OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, 0, pid) };
    if handle.is_null() {
        return Err(io::Error::last_os_error());
    }
    let handle = unsafe { OwnedHandle::from_raw_handle(handle) };
    let mut path = vec![0u16; 32768];
    let mut len = path.len() as u32;
    if unsafe { QueryFullProcessImageNameW(handle.as_raw_handle(), 0, path.as_mut_ptr(), &mut len) }
        == 0
    {
        return Err(io::Error::last_os_error());
    }
    let path = OsString::from_wide(&path[..len as usize]);
    Ok(Path::new(&path)
        .file_name()
        .map(|n| n.to_string_lossy().into_owned()))
}
impl ProcessResolver for WindowsProcessResolver {
    fn resolve(&self, src: SocketAddr, dst: SocketAddr, is_udp: bool) -> Option<String> {
        match self.try_resolve(src, dst, is_udp) {
            Ok(name) => name,
            Err(err) => {
                eprintln!("TCP process lookup for {src}: {err}");
                None
            }
        }
    }
    fn try_resolve(
        &self,
        src: SocketAddr,
        _: SocketAddr,
        is_udp: bool,
    ) -> io::Result<Option<String>> {
        if is_udp {
            return Ok(None);
        }
        process_name(src).map_err(|error| {
            io::Error::new(
                error.kind(),
                format!("Windows TCP owner for {src}: {error}"),
            )
        })
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    use std::net::{TcpListener, TcpStream};
    #[test]
    fn owns_loopback_client() {
        for addr in ["127.0.0.1:0", "[::1]:0"] {
            let listener = TcpListener::bind(addr).unwrap();
            let dst = listener.local_addr().unwrap();
            let client = TcpStream::connect(dst).unwrap();
            let (_peer, src) = listener.accept().unwrap();
            assert_eq!(client.local_addr().unwrap(), src);
            assert_eq!(
                WindowsProcessResolver.resolve(src, dst, false).unwrap(),
                std::env::current_exe()
                    .unwrap()
                    .file_name()
                    .unwrap()
                    .to_str()
                    .unwrap()
            );
        }
    }
}
