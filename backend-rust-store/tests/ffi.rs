use smartvpn_store::ffi::*;
fn bytes(out: &mut Vec<u8>, v: &[u8]) {
    out.extend_from_slice(&(v.len() as u64).to_le_bytes());
    out.extend_from_slice(v);
}
unsafe fn read(result: u64) -> Vec<u8> {
    let mut n = 0;
    assert_eq!(smartvpn_store_result_size_v1(result, &mut n), 0);
    assert!(n >= 20);
    let mut out = vec![0; n];
    let mut written = 0;
    assert_eq!(
        smartvpn_store_result_read_v1(result, out.as_mut_ptr(), out.len(), &mut written),
        0
    );
    assert_eq!(written, n);
    assert_eq!(
        u64::from_le_bytes(out[12..20].try_into().unwrap()) as usize,
        n - 20
    );
    out
}
unsafe fn open() -> u64 {
    let path: Vec<u16> = ":memory:".encode_utf16().collect();
    let (mut h, mut r) = (0, 0);
    assert_eq!(
        smartvpn_store_open_v1(path.as_ptr(), path.len(), &mut h, &mut r),
        0
    );
    assert_eq!(&read(r)[..4], &[0; 4]);
    assert_eq!(smartvpn_store_claim_v1(h, r), 0);
    assert_eq!(smartvpn_store_result_free_v1(r), 0);
    h
}
unsafe fn call(h: u64, op: u32, input: &[u8]) -> u64 {
    let mut r = 0;
    assert_eq!(
        smartvpn_store_call_v1(h, op, input.as_ptr(), input.len(), &mut r),
        0
    );
    r
}
unsafe fn close(h: u64) {
    let mut r = 0;
    assert_eq!(smartvpn_store_close_v1(h, &mut r), 0);
    assert_eq!(&read(r)[..4], &[0; 4]);
    assert_eq!(smartvpn_store_result_free_v1(r), 0);
}
#[test]
fn ffi_lifecycle_execute_once_limits_and_concurrency() {
    unsafe {
        assert_eq!(smartvpn_store_version_v1(), 1);
        let path: Vec<u16> = ":memory:".encode_utf16().collect();
        let (mut h, mut r) = (0, 0);
        assert_eq!(
            smartvpn_store_open_v1(path.as_ptr(), path.len(), &mut h, &mut r),
            0
        );
        let mut other = 0;
        assert_eq!(
            smartvpn_store_call_v1(h, 0, std::ptr::null(), 0, &mut other),
            -4
        );
        assert_eq!(smartvpn_store_result_free_v1(r), 0); // Abandoned unclaimed open closes itself.
        assert_eq!(smartvpn_store_claim_v1(h, r), -4);
        assert_eq!(
            smartvpn_store_call_v1(h, 0, std::ptr::null(), 0, &mut other),
            -4
        );
        let h = open();
        // Record one event once. Repeated reads/sizing do not rerun INSERT.
        let mut event = 42i64.to_le_bytes().to_vec();
        for v in [b"group".as_slice(), b"from", b"to", b"trigger", b"evidence"] {
            bytes(&mut event, v);
        }
        let result = call(h, 10, &event);
        let first = read(result);
        for _ in 0..4 {
            assert_eq!(read(result), first);
        }
        let mut tiny = [55u8; 1];
        let mut n = 0;
        assert_eq!(
            smartvpn_store_result_read_v1(result, tiny.as_mut_ptr(), 1, &mut n),
            -2
        );
        assert_eq!(tiny, [55]);
        assert_eq!(n, first.len());
        assert_eq!(read(result), first);
        assert_eq!(smartvpn_store_result_free_v1(result), 0);
        assert_eq!(smartvpn_store_result_free_v1(result), -4);
        let result = call(h, 11, &50i64.to_le_bytes());
        let response = read(result);
        assert_eq!(u64::from_le_bytes(response[20..28].try_into().unwrap()), 1);
        smartvpn_store_result_free_v1(result);
        // Every byte is SQLite TEXT, no lossy UTF8 conversion in transport.
        let mut kv = Vec::new();
        bytes(&mut kv, b"key\0\xff");
        bytes(&mut kv, b"value\xff\0");
        let result = call(h, 1, &kv);
        assert_eq!(&read(result)[..4], &[0; 4]);
        smartvpn_store_result_free_v1(result);
        let mut key = Vec::new();
        bytes(&mut key, b"key\0\xff");
        let result = call(h, 2, &key);
        let response = read(result);
        assert_eq!(response[20], 1);
        assert_eq!(&response[29..], b"value\xff\0");
        smartvpn_store_result_free_v1(result);
        // Input must be completely validated before a mutating operation.
        kv.push(1);
        let result = call(h, 1, &kv);
        assert_eq!(read(result)[0], 1);
        smartvpn_store_result_free_v1(result);
        // A read that exceeds the cached-result cap produces a cached limit error,
        // not a second query; reading that result repeatedly has no SQL effects.
        let huge = vec![b'x'; 6 << 20];
        for stamp in [100i64, 101, 102] {
            let mut event = stamp.to_le_bytes().to_vec();
            for v in [
                b"group".as_slice(),
                b"from",
                b"to",
                b"trigger",
                huge.as_slice(),
            ] {
                bytes(&mut event, v);
            }
            let result = call(h, 10, &event);
            assert_eq!(read(result)[0], 0);
            smartvpn_store_result_free_v1(result);
        }
        let result = call(h, 11, &3i64.to_le_bytes());
        let error = read(result);
        assert_eq!(error[0], 4);
        assert_eq!(error[8], 0);
        assert_eq!(read(result), error);
        smartvpn_store_result_free_v1(result);
        let result = call(h, 11, &1i64.to_le_bytes());
        let row = read(result);
        assert_eq!(row[0], 0);
        assert_eq!(u64::from_le_bytes(row[20..28].try_into().unwrap()), 1);
        assert_eq!(i64::from_le_bytes(row[28..36].try_into().unwrap()), 102);
        smartvpn_store_result_free_v1(result);
        // Fill result slots; next mutation is rejected before execution.
        let mut results = Vec::new();
        for _ in 0..256 {
            results.push(call(h, 0, &[]));
        }
        let mut input = Vec::new();
        bytes(&mut input, b"must-not-exist");
        bytes(&mut input, b"v");
        let mut result = 99;
        assert_eq!(
            smartvpn_store_call_v1(h, 1, input.as_ptr(), input.len(), &mut result),
            -3
        );
        assert_eq!(result, 0);
        for r in results {
            assert_eq!(smartvpn_store_result_free_v1(r), 0);
        }
        let mut key = Vec::new();
        bytes(&mut key, b"must-not-exist");
        let mut result = call(h, 2, &key);
        assert_eq!(&read(result)[20..], &[0]);
        smartvpn_store_result_free_v1(result);
        close(h);
        assert_eq!(
            smartvpn_store_call_v1(h, 0, std::ptr::null(), 0, &mut result),
            -4
        );
        // Calls racing close either own a live store through completion or see stale ID.
        let h = open();
        let mut workers = Vec::new();
        for _ in 0..8 {
            workers.push(std::thread::spawn(move || {
                let mut r = 0;
                let status = smartvpn_store_call_v1(h, 0, std::ptr::null(), 0, &mut r);
                assert!(status == 0 || status == -4);
                if status == 0 {
                    assert_eq!(smartvpn_store_result_free_v1(r), 0);
                }
            }));
        }
        close(h);
        for w in workers {
            w.join().unwrap();
        }
        // Stores are bounded; rejected open has no owned store/result.
        let mut handles = Vec::new();
        for _ in 0..64 {
            handles.push(open());
        }
        let (mut rejected, mut reply) = (99, 99);
        assert_eq!(
            smartvpn_store_open_v1(path.as_ptr(), path.len(), &mut rejected, &mut reply),
            -3
        );
        assert_eq!((rejected, reply), (0, 0));
        for h in handles {
            close(h);
        }
        let mut n = 7;
        assert_eq!(smartvpn_store_result_size_v1(0, &mut n), -4);
        assert_eq!(n, 0);
        assert_eq!(
            smartvpn_store_open_v1([0u16].as_ptr(), 1, &mut rejected, &mut reply),
            -1
        );
    }
}
