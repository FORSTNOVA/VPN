use smartvpn_core::http_core::*;
#[test]
fn authorization_bytes() {
    for token in [b"".as_slice(), b"sample", b"\xff\x00", "中文".as_bytes()] {
        let mut header = b"Bearer ".to_vec();
        header.extend_from_slice(token);
        assert!(authorized(&header, token));
        assert!(!authorized(b"bearer sample", token));
        header.push(b' ');
        assert!(!authorized(&header, token));
    }
}
#[test]
fn canonical_route_dispatch() {
    assert_eq!(ROUTES.len(), 50);
    for (i, (method, path)) in ROUTES.iter().enumerate() {
        assert_eq!(
            dispatch(method.as_bytes(), path.as_bytes(), true),
            Dispatch::Route(i + 1)
        );
        if *method == "GET" {
            assert_eq!(
                dispatch(b"HEAD", path.as_bytes(), true),
                Dispatch::Route(i + 1)
            );
        }
        assert_eq!(
            dispatch(b"OPTIONS", path.as_bytes(), true),
            Dispatch::MethodNotAllowed
        );
    }
    assert_eq!(
        dispatch(b"POST", b"/api/tun/wintun", false),
        Dispatch::NotFound
    );
    assert_eq!(dispatch(b"GET", b"/api/unknown", true), Dispatch::NotFound);
    assert_eq!(dispatch(b"GET", b"/api/state/", true), Dispatch::NotFound);
}
#[test]
fn ffi_contracts() {
    unsafe {
        assert_eq!(
            smartvpn_http_authorized_v1(std::ptr::null(), 1, std::ptr::null(), 0),
            -1
        );
        assert_eq!(
            smartvpn_http_authorized_v1(std::ptr::null(), 0, std::ptr::null(), 0),
            0
        );
        assert_eq!(
            smartvpn_http_authorized_v1(b"Bearer ".as_ptr(), 7, std::ptr::null(), 0),
            1
        );
        let mut out = [0x55; 64];
        let mut n = 0;
        assert_eq!(
            smartvpn_http_identity_v1(0, out.as_mut_ptr(), 1, &mut n),
            -2
        );
        assert_eq!(out, [0x55; 64]);
        assert_eq!(n, identity_json(false).len());
        assert_eq!(
            smartvpn_http_identity_v1(2, out.as_mut_ptr(), 64, &mut n),
            -1
        );
        for connected in [false, true] {
            assert_eq!(
                smartvpn_http_identity_v1(u32::from(connected), out.as_mut_ptr(), 64, &mut n),
                0
            );
            assert_eq!(&out[..n], identity_json(connected));
        }
        assert_eq!(
            smartvpn_http_dispatch_v1(b"GET".as_ptr(), 3, b"/api/state".as_ptr(), 10, 1),
            2
        );
        assert_eq!(
            smartvpn_http_dispatch_v1(std::ptr::null(), 1, std::ptr::null(), 0, 1),
            -1
        );
    }
}

#[test]
fn request_plans_never_execute_business_handlers() {
    let c = RequestContext {
        token: b"synthetic",
        connected: true,
        windows: true,
    };
    assert_eq!(
        plan_request(&c, b"GET", b"/api/identify", b""),
        ResponsePlan::Identity(identity_json(true))
    );
    assert_eq!(
        plan_request(&c, b"GET", b"/api/connect", b""),
        ResponsePlan::MethodNotAllowed
    );
    assert_eq!(
        plan_request(&c, b"GET", b"/api/unknown", b""),
        ResponsePlan::NotFound
    );
    for (i, (method, path)) in ROUTES.iter().enumerate().skip(1) {
        assert_eq!(
            plan_request(&c, method.as_bytes(), path.as_bytes(), b""),
            ResponsePlan::Unauthorized
        );
        assert_eq!(
            plan_request(&c, method.as_bytes(), path.as_bytes(), b"Bearer synthetic"),
            ResponsePlan::Delegate { route_id: i + 1 }
        );
    }
}

#[test]
fn app_reachable_selection() {
    assert_eq!(choose_reachable(&[], None), None);
    assert_eq!(choose_reachable(&[-1, 0], Some(0)), None);
    assert_eq!(choose_reachable(&[20, 10, 10], None), Some(1));
    assert_eq!(choose_reachable(&[20, 10, 10], Some(0)), Some(0));
    assert_eq!(choose_reachable(&[0, 10, 20], Some(0)), Some(1));
    unsafe {
        assert_eq!(smartvpn_choose_reachable_v1(std::ptr::null(), 0, -1), -1);
        assert_eq!(smartvpn_choose_reachable_v1(std::ptr::null(), 1, -1), -2);
        assert_eq!(smartvpn_choose_reachable_v1([5i64].as_ptr(), 1, 1), -2);
    }
}
