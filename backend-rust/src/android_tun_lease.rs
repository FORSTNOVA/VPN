// Storage belongs to the caller and is protected by the tunnel mutex. Rust
// returns the descriptor to close; it never takes a second OS file handle.
#[repr(C)]
#[derive(Default)]
pub struct Lease {
    generation: u64,
    fd: i32,
    flags: u32,
}

const AUTHORIZED: u32 = 1;
const KERNEL_OWNS: u32 = 2;

/// The pointer must reference an exclusive Lease for the duration of the call.
/// Unknown operations leave the lease untouched and return usize::MAX.
#[no_mangle]
pub unsafe extern "C" fn smartvpn_android_tun_lease_v1(
    state: *mut Lease,
    op: usize,
    fd: usize,
    authorized: usize,
) -> usize {
    let Some(state) = state.as_mut() else {
        return usize::MAX;
    };
    match op {
        1 => {
            let fd = fd as i32;
            if state.fd != fd {
                state.generation = state.generation.wrapping_add(1);
                state.flags = 0;
            }
            state.fd = fd;
            state.flags =
                (state.flags & KERNEL_OWNS) | if authorized != 0 { AUTHORIZED } else { 0 };
        }
        2 => state.flags |= KERNEL_OWNS,
        3 => {
            let orphan = if state.flags & KERNEL_OWNS == 0 {
                state.fd.max(0)
            } else {
                0
            };
            state.fd = 0;
            state.flags = 0;
            state.generation = state.generation.wrapping_add(1);
            return orphan as usize;
        }
        _ => return usize::MAX,
    }
    0
}
