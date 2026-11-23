extern crate smartvpn_core as _;
use std::ptr;

extern "C" {
    fn smartvpn_cached_nodes_v1(
        op: usize,
        handle: usize,
        index: usize,
        data: *mut u8,
        len: usize,
    ) -> usize;
}

fn replace(handle: usize, data: &[u8]) -> usize {
    unsafe { smartvpn_cached_nodes_v1(1, handle, 0, data.as_ptr().cast_mut(), data.len()) }
}

#[test]
fn bad_input_and_short_reads_preserve_the_last_complete_batch() {
    let handle = unsafe { smartvpn_cached_nodes_v1(0, 0, 0, ptr::null_mut(), 0) };
    let mut first = vec![1];
    for field in ["label", "name", "type", "network"] {
        first.extend_from_slice(&(field.len() as u64).to_le_bytes());
        first.extend_from_slice(field.as_bytes());
    }
    first.push(63);
    let mut second = first.clone();
    second[9] = b'L';
    *second.last_mut().unwrap() = 0;
    assert_eq!(replace(handle, &first), 0);
    assert_eq!(replace(handle, &first[..first.len() - 1]), usize::MAX);
    let mut short = [57u8; 5];
    assert_eq!(
        unsafe { smartvpn_cached_nodes_v1(2, handle, 0, short.as_mut_ptr(), short.len()) },
        first.len()
    );
    assert_eq!(short, [57; 5]);
    let mut prior = vec![0; first.len()];
    assert_eq!(
        unsafe { smartvpn_cached_nodes_v1(2, handle, 0, prior.as_mut_ptr(), prior.len()) },
        first.len()
    );
    assert_eq!(prior, first);
    std::thread::scope(|scope| {
        scope.spawn(|| {
            for _ in 0..500 {
                assert_eq!(replace(handle, &second), 0);
                assert_eq!(replace(handle, &first), 0);
            }
        });
        let mut out = vec![0; first.len()];
        for _ in 0..1000 {
            assert_eq!(
                unsafe { smartvpn_cached_nodes_v1(2, handle, 0, out.as_mut_ptr(), out.len()) },
                out.len()
            );
            assert!(out == first || out == second);
        }
    });
    assert_eq!(
        unsafe { smartvpn_cached_nodes_v1(4, handle, 0, ptr::null_mut(), 0) },
        0
    );
}
