use super::TunDevice;
use crate::dialer::BoxFuture;
use std::{ffi::c_void, io, os::windows::ffi::OsStrExt, ptr, slice, time::Duration};
use windows_sys::Win32::{
    Foundation::{FreeLibrary, ERROR_NO_MORE_ITEMS, HMODULE},
    System::LibraryLoader::{
        GetProcAddress, LoadLibraryExW, LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR,
        LOAD_LIBRARY_SEARCH_SYSTEM32,
    },
};

type Handle = *mut c_void;
type Create =
    unsafe extern "system" fn(*const u16, *const u16, *const windows_sys::core::GUID) -> Handle;
type Open = unsafe extern "system" fn(*const u16) -> Handle;
type Start = unsafe extern "system" fn(Handle, u32) -> Handle;
type Close = unsafe extern "system" fn(Handle);
type Receive = unsafe extern "system" fn(Handle, *mut u32) -> *mut u8;
type Allocate = unsafe extern "system" fn(Handle, u32) -> *mut u8;
type Packet = unsafe extern "system" fn(Handle, *mut u8);
struct Library(HMODULE);
impl Drop for Library {
    fn drop(&mut self) {
        unsafe { FreeLibrary(self.0) };
    }
}
struct Api {
    create: Create,
    open: Open,
    start: Start,
    close: Close,
    end: Close,
    recv: Receive,
    release: Packet,
    allocate: Allocate,
    send: Packet,
    _library: Library,
}
impl Api {
    fn load() -> io::Result<Self> {
        let path = std::env::current_exe()?.with_file_name("wintun.dll");
        let wide: Vec<_> = path.as_os_str().encode_wide().chain(Some(0)).collect();
        let module = unsafe {
            LoadLibraryExW(
                wide.as_ptr(),
                ptr::null_mut(),
                LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR | LOAD_LIBRARY_SEARCH_SYSTEM32,
            )
        };
        if module.is_null() {
            return Err(io::Error::new(
                io::ErrorKind::NotFound,
                format!("{}: {}", path.display(), io::Error::last_os_error()),
            ));
        }
        let library = Library(module);
        macro_rules! symbol {
            ($name:literal,$ty:ty) => {{
                let raw = unsafe { GetProcAddress(module, concat!($name, "\0").as_ptr()) }
                    .ok_or_else(|| {
                        io::Error::other(format!("{}: {}", $name, io::Error::last_os_error()))
                    })?;
                unsafe { std::mem::transmute::<unsafe extern "system" fn() -> isize, $ty>(raw) }
            }};
        }
        Ok(Self {
            create: symbol!("WintunCreateAdapter", Create),
            open: symbol!("WintunOpenAdapter", Open),
            start: symbol!("WintunStartSession", Start),
            end: symbol!("WintunEndSession", Close),
            close: symbol!("WintunCloseAdapter", Close),
            recv: symbol!("WintunReceivePacket", Receive),
            release: symbol!("WintunReleaseReceivePacket", Packet),
            allocate: symbol!("WintunAllocateSendPacket", Allocate),
            send: symbol!("WintunSendPacket", Packet),
            _library: library,
        })
    }
}
pub struct WintunDevice {
    api: Api,
    adapter: Handle,
    session: Handle,
}
// Wintun packet APIs are thread-safe. The session and module outlive every borrowed packet.
unsafe impl Send for WintunDevice {}
unsafe impl Sync for WintunDevice {}
impl WintunDevice {
    pub fn open(name: &str) -> io::Result<Self> {
        Self::start(name, false)
    }
    pub fn create(name: &str) -> io::Result<Self> {
        Self::start(name, true)
    }
    fn start(name: &str, create: bool) -> io::Result<Self> {
        if name.is_empty() || name.contains('\0') {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "invalid adapter name",
            ));
        }
        let name: Vec<_> = name.encode_utf16().chain(Some(0)).collect();
        let api = Api::load()?;
        let tunnel_type: Vec<_> = "SmartVPN".encode_utf16().chain(Some(0)).collect();
        let adapter = unsafe {
            if create {
                (api.create)(name.as_ptr(), tunnel_type.as_ptr(), ptr::null())
            } else {
                (api.open)(name.as_ptr())
            }
        };
        if adapter.is_null() {
            return Err(io::Error::last_os_error());
        }
        let session = unsafe { (api.start)(adapter, 0x400000) };
        if session.is_null() {
            let err = io::Error::last_os_error();
            unsafe { (api.close)(adapter) };
            return Err(err);
        }
        Ok(Self {
            api,
            adapter,
            session,
        })
    }
    fn receive(&self) -> io::Result<Vec<u8>> {
        let mut size = 0;
        let p = unsafe { (self.api.recv)(self.session, &mut size) };
        if p.is_null() {
            return Err(io::Error::last_os_error());
        }
        let packet = unsafe { slice::from_raw_parts(p, size as usize).to_vec() };
        unsafe { (self.api.release)(self.session, p) };
        Ok(packet)
    }
}
impl Drop for WintunDevice {
    fn drop(&mut self) {
        unsafe {
            (self.api.end)(self.session);
            (self.api.close)(self.adapter)
        }
    }
}
impl TunDevice for WintunDevice {
    fn recv_packet(&mut self) -> BoxFuture<'_, io::Result<Vec<u8>>> {
        Box::pin(async {
            loop {
                match self.receive() {
                    Err(e) if e.raw_os_error() == Some(ERROR_NO_MORE_ITEMS as i32) => {
                        tokio::time::sleep(Duration::from_millis(2)).await
                    }
                    result => return result,
                }
            }
        })
    }
    fn send_packet<'a>(&'a self, pkt: &'a [u8]) -> BoxFuture<'a, io::Result<()>> {
        Box::pin(async {
            if pkt.len() > 65535 {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidInput,
                    "oversized Wintun packet",
                ));
            }
            let p = unsafe { (self.api.allocate)(self.session, pkt.len() as u32) };
            if p.is_null() {
                return Err(io::Error::last_os_error());
            }
            unsafe {
                ptr::copy_nonoverlapping(pkt.as_ptr(), p, pkt.len());
                (self.api.send)(self.session, p)
            };
            Ok(())
        })
    }
}
