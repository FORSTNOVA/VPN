use std::{net::SocketAddr, sync::Arc};

use aes_gcm::{
    aead::{Aead, KeyInit, Payload},
    Aes128Gcm, Aes256Gcm, Nonce,
};
use base64::{engine::general_purpose::URL_SAFE_NO_PAD, Engine as _};
use bytes::{BufMut, Bytes, BytesMut};
use futures_util::{SinkExt, StreamExt};
use hkdf::{
    hmac::{Hmac, Mac},
    Hkdf,
};
use rand::RngCore;
use rustls::pki_types::{CertificateDer, PrivateKeyDer, PrivatePkcs8KeyDer};
use sha2::{Digest, Sha256};
use smartvpn_engine::{
    dialer::{OutboundDialer, TargetAddr},
    protocol::vless::{VlessConfig, VlessDialer, VlessNetwork, VlessSecurity},
    transport::{
        grpc::GrpcConfig,
        reality::RealityConfig,
        tls::{ensure_crypto_provider, TlsConfig},
        ws::WsConfig,
    },
};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::{TcpListener, TcpStream},
};
use tokio_rustls::TlsAcceptor;
use tokio_tungstenite::{accept_async, tungstenite::Message};
use x25519_dalek::{PublicKey, StaticSecret};

fn test_tls_acceptor() -> Arc<TlsAcceptor> {
    ensure_crypto_provider();
    let cert = rcgen::generate_simple_self_signed(vec!["localhost".to_string()]).unwrap();
    let cert_der = CertificateDer::from(cert.cert.der().to_vec());
    let key_der = PrivateKeyDer::Pkcs8(PrivatePkcs8KeyDer::from(cert.key_pair.serialize_der()));
    let cfg = rustls::ServerConfig::builder()
        .with_no_client_auth()
        .with_single_cert(vec![cert_der], key_der)
        .unwrap();
    Arc::new(TlsAcceptor::from(Arc::new(cfg)))
}

async fn spawn_echo_target() -> SocketAddr {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        while let Ok((mut s, _)) = listener.accept().await {
            tokio::spawn(async move {
                let mut buf = [0u8; 4096];
                while let Ok(n) = s.read(&mut buf).await {
                    if n == 0 {
                        break;
                    }
                    if s.write_all(&buf[..n]).await.is_err() {
                        break;
                    }
                }
            });
        }
    });
    addr
}

async fn read_vless_header(data: &[u8], expected_uuid: &[u8; 16]) -> (TargetAddr, usize) {
    assert_eq!(data[0], 0x00);
    assert_eq!(&data[1..17], expected_uuid);
    let addons_len = data[17] as usize;
    let mut pos = 18 + addons_len;
    let cmd = data[pos];
    assert_eq!(cmd, 0x01);
    pos += 1;
    let port = u16::from_be_bytes([data[pos], data[pos + 1]]);
    pos += 2;
    let atyp = data[pos];
    pos += 1;
    let target = match atyp {
        0x01 => {
            let ip = std::net::Ipv4Addr::new(data[pos], data[pos + 1], data[pos + 2], data[pos + 3]);
            pos += 4;
            TargetAddr::Ip(SocketAddr::from((ip, port)))
        }
        0x02 => {
            let dlen = data[pos] as usize;
            pos += 1;
            let host = String::from_utf8(data[pos..pos + dlen].to_vec()).unwrap();
            pos += dlen;
            TargetAddr::Domain(host, port)
        }
        other => panic!("unexpected vless atyp {other}"),
    };
    (target, pos)
}

async fn spawn_vless_ws_server(uuid: [u8; 16]) -> SocketAddr {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let acceptor = test_tls_acceptor();
    tokio::spawn(async move {
        while let Ok((tcp, _)) = listener.accept().await {
            let acceptor = acceptor.clone();
            tokio::spawn(async move {
                let tls = acceptor.accept(tcp).await.unwrap();
                let mut ws = accept_async(tls).await.unwrap();
                let first = match ws.next().await.unwrap().unwrap() {
                    Message::Binary(b) => b.to_vec(),
                    other => panic!("expected binary, got {other:?}"),
                };
                let (target, hdr_end) = read_vless_header(&first, &uuid).await;
                let mut up = match target {
                    TargetAddr::Ip(a) => TcpStream::connect(a).await.unwrap(),
                    TargetAddr::Domain(h, p) => TcpStream::connect((h.as_str(), p)).await.unwrap(),
                };
                // Send VLESS response header [0x00, 0x00]
                let mut resp_hdr = vec![0x00u8, 0x00u8];
                if hdr_end < first.len() {
                    up.write_all(&first[hdr_end..]).await.unwrap();
                    let mut buf = vec![0u8; first.len() - hdr_end];
                    up.read_exact(&mut buf).await.unwrap();
                    resp_hdr.extend_from_slice(&buf);
                }
                ws.send(Message::Binary(resp_hdr.into())).await.unwrap();

                while let Some(Ok(msg)) = ws.next().await {
                    if let Message::Binary(data) = msg {
                        up.write_all(&data).await.unwrap();
                        let mut echo = vec![0u8; data.len()];
                        up.read_exact(&mut echo).await.unwrap();
                        ws.send(Message::Binary(echo.into())).await.unwrap();
                    } else if matches!(msg, Message::Close(_)) {
                        break;
                    }
                }
            });
        }
    });
    addr
}

async fn spawn_vless_grpc_server(uuid: [u8; 16]) -> SocketAddr {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let acceptor = test_tls_acceptor();
    tokio::spawn(async move {
        while let Ok((tcp, _)) = listener.accept().await {
            let acceptor = acceptor.clone();
            tokio::spawn(async move {
                let tls = acceptor.accept(tcp).await.unwrap();
                let mut h2_conn = h2::server::handshake(tls).await.unwrap();
                while let Some(Ok((req, mut respond))) = h2_conn.accept().await {
                    tokio::spawn(async move {
                        let mut body = req.into_body();
                        let mut send = respond
                            .send_response(http::Response::new(()), false)
                            .unwrap();
                        let mut buf = BytesMut::new();
                        let mut up: Option<TcpStream> = None;
                        let mut resp_sent = false;
                        while let Some(Ok(chunk)) = body.data().await {
                            let _ = body.flow_control().release_capacity(chunk.len());
                            buf.extend_from_slice(&chunk);
                            while buf.len() >= 5 {
                                let len =
                                    u32::from_be_bytes([buf[1], buf[2], buf[3], buf[4]]) as usize;
                                if buf.len() < 5 + len {
                                    break;
                                }
                                let _ = buf.split_to(5);
                                let msg = buf.split_to(len);
                                assert_eq!(msg[0], 0x0a);
                                let data_len = msg[1] as usize;
                                let payload = &msg[2..2 + data_len];

                                let mut out = Vec::new();
                                if up.is_none() {
                                    let (target, hdr_end) = read_vless_header(payload, &uuid).await;
                                    let mut u = match target {
                                        TargetAddr::Ip(a) => TcpStream::connect(a).await.unwrap(),
                                        TargetAddr::Domain(h, p) => {
                                            TcpStream::connect((h.as_str(), p)).await.unwrap()
                                        }
                                    };
                                    if !resp_sent {
                                        out.extend_from_slice(&[0x00, 0x00]);
                                        resp_sent = true;
                                    }
                                    if hdr_end < payload.len() {
                                        u.write_all(&payload[hdr_end..]).await.unwrap();
                                        let mut echo = vec![0u8; payload.len() - hdr_end];
                                        u.read_exact(&mut echo).await.unwrap();
                                        out.extend_from_slice(&echo);
                                    }
                                    up = Some(u);
                                } else if let Some(u) = up.as_mut() {
                                    u.write_all(payload).await.unwrap();
                                    let mut echo = vec![0u8; payload.len()];
                                    u.read_exact(&mut echo).await.unwrap();
                                    out.extend_from_slice(&echo);
                                }
                                if !out.is_empty() {
                                    let mut proto = BytesMut::new();
                                    proto.put_u8(0x0a);
                                    proto.put_u8(out.len() as u8);
                                    proto.extend_from_slice(&out);
                                    let mut frame = BytesMut::new();
                                    frame.put_u8(0x00);
                                    frame.put_u32(proto.len() as u32);
                                    frame.extend_from_slice(&proto);
                                    send.send_data(frame.freeze(), false).unwrap();
                                }
                            }
                        }
                        let _ = send.send_data(Bytes::new(), true);
                    });
                }
            });
        }
    });
    addr
}

fn hkdf_expand_label(secret: &[u8], label: &[u8], context: &[u8], len: usize) -> Vec<u8> {
    let mut hl = Vec::new();
    hl.extend_from_slice(&(len as u16).to_be_bytes());
    hl.push((6 + label.len()) as u8);
    hl.extend_from_slice(b"tls13 ");
    hl.extend_from_slice(label);
    hl.push(context.len() as u8);
    hl.extend_from_slice(context);
    let hk = Hkdf::<Sha256>::from_prk(secret).unwrap();
    let mut out = vec![0u8; len];
    hk.expand(&hl, &mut out).unwrap();
    out
}

fn derive_secret(secret: &[u8], label: &[u8], hash: &[u8]) -> Vec<u8> {
    hkdf_expand_label(secret, label, hash, 32)
}

fn seal_tls13(key: &[u8], iv: &[u8; 12], seq: &mut u64, rec_type: u8, payload: &[u8]) -> Vec<u8> {
    let mut inner = payload.to_vec();
    inner.push(rec_type);
    let total_len = (inner.len() + 16) as u16;
    let aad = [0x17, 0x03, 0x03, (total_len >> 8) as u8, total_len as u8];
    let mut nonce = *iv;
    let sb = seq.to_be_bytes();
    for i in 0..8 {
        nonce[4 + i] ^= sb[i];
    }
    *seq += 1;
    let ct = Aes128Gcm::new_from_slice(key)
        .unwrap()
        .encrypt(
            Nonce::from_slice(&nonce),
            Payload {
                msg: &inner,
                aad: &aad,
            },
        )
        .unwrap();
    let mut out = aad.to_vec();
    out.extend_from_slice(&ct);
    out
}

fn open_tls13(key: &[u8], iv: &[u8; 12], seq: &mut u64, hdr: &[u8; 5], ct: &[u8]) -> (u8, Vec<u8>) {
    let mut nonce = *iv;
    let sb = seq.to_be_bytes();
    for i in 0..8 {
        nonce[4 + i] ^= sb[i];
    }
    *seq += 1;
    let mut plain = Aes128Gcm::new_from_slice(key)
        .unwrap()
        .decrypt(Nonce::from_slice(&nonce), Payload { msg: ct, aad: hdr })
        .unwrap();
    while plain.last() == Some(&0) {
        plain.pop();
    }
    let rt = plain.pop().unwrap();
    (rt, plain)
}

#[derive(Clone, Copy, PartialEq)]
enum ServerProof {
    Valid,
    BadCertificateMac,
    BadTranscriptSignature,
}

async fn spawn_reality_vless_vision_server(
    server_priv: StaticSecret,
    expected_sid: [u8; 4],
    proof_case: ServerProof,
    uuid: [u8; 16],
) -> SocketAddr {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        while let Ok((mut s, _)) = listener.accept().await {
            let server_priv = server_priv.clone();
            tokio::spawn(async move {
                let mut ch_hdr = [0u8; 5];
                s.read_exact(&mut ch_hdr).await.unwrap();
                let ch_len = u16::from_be_bytes([ch_hdr[3], ch_hdr[4]]) as usize;
                let mut ch_hs = vec![0u8; ch_len];
                s.read_exact(&mut ch_hs).await.unwrap();

                let body = &ch_hs[4..];
                let client_random: [u8; 32] = body[2..34].try_into().unwrap();
                let sid_len = body[34] as usize;
                assert_eq!(sid_len, 32);
                let sid = &body[35..67];

                // Extract client X25519 key_share from extensions
                let cs_len = u16::from_be_bytes([body[67], body[68]]) as usize;
                let comp_pos = 69 + cs_len;
                let comp_len = body[comp_pos] as usize;
                let ext_start = comp_pos + 1 + comp_len + 2;
                let mut pos = ext_start;
                let mut client_pub = None;
                while pos + 4 <= body.len() {
                    let k = u16::from_be_bytes([body[pos], body[pos + 1]]);
                    let l = u16::from_be_bytes([body[pos + 2], body[pos + 3]]) as usize;
                    pos += 4;
                    if k == 0x0033 {
                        let key: [u8; 32] = body[pos + 6..pos + 38].try_into().unwrap();
                        client_pub = Some(PublicKey::from(key));
                    }
                    pos += l;
                }
                let client_pub = client_pub.unwrap();

                // Verify Reality session_id using server_priv
                let auth_shared = server_priv.diffie_hellman(&client_pub);
                let mut auth_key = [0u8; 32];
                Hkdf::<Sha256>::new(Some(&client_random[..20]), auth_shared.as_bytes())
                    .expand(b"REALITY", &mut auth_key).unwrap();
                let mut ch_zero = ch_hs.clone();
                ch_zero[4 + 35..4 + 67].fill(0);
                let plain_sid = Aes256Gcm::new_from_slice(&auth_key)
                    .unwrap()
                    .decrypt(
                        Nonce::from_slice(&client_random[20..32]),
                        Payload {
                            msg: sid,
                            aad: &ch_zero,
                        },
                    )
                    .unwrap();
                assert_eq!(&plain_sid[8..12], &expected_sid);

                // Complete TLS 1.3 handshake
                let mut transcript = Sha256::new();
                transcript.update(&ch_hs);

                let mut srv_random = [0u8; 32];
                rand::thread_rng().fill_bytes(&mut srv_random);
                let srv_eph = StaticSecret::random_from_rng(rand::thread_rng());
                let srv_pub = PublicKey::from(&srv_eph);
                let mut sh_body = Vec::new();
                sh_body.extend_from_slice(&[0x03, 0x03]);
                sh_body.extend_from_slice(&srv_random);
                sh_body.push(32);
                sh_body.extend_from_slice(sid);
                sh_body.extend_from_slice(&[0x13, 0x01, 0x00]); // TLS_AES_128_GCM_SHA256
                let mut sh_exts = Vec::new();
                sh_exts.extend_from_slice(&[0x00, 0x2b, 0x00, 0x02, 0x03, 0x04]);
                sh_exts.extend_from_slice(&[0x00, 0x33, 0x00, 0x24, 0x00, 0x1d, 0x00, 0x20]);
                sh_exts.extend_from_slice(srv_pub.as_bytes());
                sh_body.extend_from_slice(&(sh_exts.len() as u16).to_be_bytes());
                sh_body.extend_from_slice(&sh_exts);

                let mut sh_hs = vec![0x02u8];
                sh_hs.extend_from_slice(&(sh_body.len() as u32).to_be_bytes()[1..4]);
                sh_hs.extend_from_slice(&sh_body);
                transcript.update(&sh_hs);
                let hello_hash: [u8; 32] = transcript.clone().finalize().into();

                let mut sh_rec = vec![0x16, 0x03, 0x03];
                sh_rec.extend_from_slice(&(sh_hs.len() as u16).to_be_bytes());
                sh_rec.extend_from_slice(&sh_hs);
                s.write_all(&sh_rec).await.unwrap();

                let tls_shared = srv_eph.diffie_hellman(&client_pub);
                let zeros = [0u8; 32];
                let (early, _) = Hkdf::<Sha256>::extract(Some(&zeros), &zeros);
                let derived = derive_secret(&early, b"derived", &Sha256::digest([]));
                let (hs_sec, _) = Hkdf::<Sha256>::extract(Some(&derived), tls_shared.as_bytes());
                let c_hs_sec = derive_secret(&hs_sec, b"c hs traffic", &hello_hash);
                let s_hs_sec = derive_secret(&hs_sec, b"s hs traffic", &hello_hash);

                let s_hs_key = hkdf_expand_label(&s_hs_sec, b"key", &[], 16);
                let s_hs_iv: [u8; 12] = hkdf_expand_label(&s_hs_sec, b"iv", &[], 12).try_into().unwrap();
                let mut s_hs_seq = 0u64;

                let ee = [0x08u8, 0x00, 0x00, 0x02, 0x00, 0x00];
                transcript.update(&ee);
                let ee_rec = seal_tls13(&s_hs_key, &s_hs_iv, &mut s_hs_seq, 0x16, &ee);
                s.write_all(&ee_rec).await.unwrap();

                let signing_key = rcgen::KeyPair::generate_for(&rcgen::PKCS_ED25519).unwrap();
                let cert = rcgen::CertificateParams::new(vec!["localhost".into()]).unwrap()
                    .self_signed(&signing_key).unwrap();
                let mut der = cert.der().to_vec();
                let sig_start = der.len() - 64;
                let mut mac = <Hmac<sha2::Sha512> as Mac>::new_from_slice(&auth_key).unwrap();
                mac.update(signing_key.public_key_raw());
                der[sig_start..].copy_from_slice(&mac.finalize().into_bytes());
                if proof_case == ServerProof::BadCertificateMac { der[sig_start] ^= 1; }
                let mut cert_body = vec![0];
                cert_body.extend_from_slice(&((der.len()+5) as u32).to_be_bytes()[1..]);
                cert_body.extend_from_slice(&(der.len() as u32).to_be_bytes()[1..]);
                cert_body.extend_from_slice(&der);
                cert_body.extend_from_slice(&[0,0]);
                let mut cert_msg = vec![0x0b];
                cert_msg.extend_from_slice(&(cert_body.len() as u32).to_be_bytes()[1..]);
                cert_msg.extend_from_slice(&cert_body);
                transcript.update(&cert_msg);
                let mut signed = vec![0x20; 64];
                signed.extend_from_slice(b"TLS 1.3, server CertificateVerify\0");
                signed.extend_from_slice(&transcript.clone().finalize());
                let ed = ring::signature::Ed25519KeyPair::from_pkcs8(&signing_key.serialize_der()).unwrap();
                let mut proof = vec![0x0f,0,0,68,8,7,0,64];
                proof.extend_from_slice(ed.sign(&signed).as_ref());
                if proof_case == ServerProof::BadTranscriptSignature { proof[8] ^= 1; }
                transcript.update(&proof);
                cert_msg.extend_from_slice(&proof);
                let cert_rec = seal_tls13(&s_hs_key, &s_hs_iv, &mut s_hs_seq, 0x16, &cert_msg);
                s.write_all(&cert_rec).await.unwrap();

                if proof_case != ServerProof::Valid { return; }

                let pre_fin: [u8; 32] = transcript.clone().finalize().into();
                let s_fin_key = hkdf_expand_label(&s_hs_sec, b"finished", &[], 32);
                let mut mac = <Hmac<Sha256> as Mac>::new_from_slice(&s_fin_key).unwrap();
                mac.update(&pre_fin);
                let mut fin_msg = vec![0x14, 0x00, 0x00, 0x20];
                fin_msg.extend_from_slice(&mac.finalize().into_bytes());
                transcript.update(&fin_msg);
                let fin_rec = seal_tls13(&s_hs_key, &s_hs_iv, &mut s_hs_seq, 0x16, &fin_msg);
                s.write_all(&fin_rec).await.unwrap();

                let fin_hash: [u8; 32] = transcript.finalize().into();
                let derived2 = derive_secret(&hs_sec, b"derived", &Sha256::digest([]));
                let (master, _) = Hkdf::<Sha256>::extract(Some(&derived2), &zeros);
                let c_app_sec = derive_secret(&master, b"c ap traffic", &fin_hash);
                let s_app_sec = derive_secret(&master, b"s ap traffic", &fin_hash);

                // Read client CCS + Finished
                let mut ccs = [0u8; 6];
                s.read_exact(&mut ccs).await.unwrap();
                let mut c_fin_hdr = [0u8; 5];
                s.read_exact(&mut c_fin_hdr).await.unwrap();
                let c_fin_len = u16::from_be_bytes([c_fin_hdr[3], c_fin_hdr[4]]) as usize;
                let mut c_fin_ct = vec![0u8; c_fin_len];
                s.read_exact(&mut c_fin_ct).await.unwrap();
                let c_hs_key = hkdf_expand_label(&c_hs_sec, b"key", &[], 16);
                let c_hs_iv: [u8; 12] = hkdf_expand_label(&c_hs_sec, b"iv", &[], 12).try_into().unwrap();
                let mut c_hs_seq = 0u64;
                let (rt, _) = open_tls13(&c_hs_key, &c_hs_iv, &mut c_hs_seq, &c_fin_hdr, &c_fin_ct);
                assert_eq!(rt, 0x16);

                let c_app_key = hkdf_expand_label(&c_app_sec, b"key", &[], 16);
                let c_app_iv: [u8; 12] = hkdf_expand_label(&c_app_sec, b"iv", &[], 12).try_into().unwrap();
                let mut c_app_seq = 0u64;
                let s_app_key = hkdf_expand_label(&s_app_sec, b"key", &[], 16);
                let s_app_iv: [u8; 12] = hkdf_expand_label(&s_app_sec, b"iv", &[], 12).try_into().unwrap();
                let mut s_app_seq = 0u64;

                // Read VLESS header
                let mut hdr = [0u8; 5];
                s.read_exact(&mut hdr).await.unwrap();
                let clen = u16::from_be_bytes([hdr[3], hdr[4]]) as usize;
                let mut ct = vec![0u8; clen];
                s.read_exact(&mut ct).await.unwrap();
                let (_, vless_hdr) = open_tls13(&c_app_key, &c_app_iv, &mut c_app_seq, &hdr, &ct);
                let (target, _) = read_vless_header(&vless_hdr, &uuid).await;
                let mut up = match target {
                    TargetAddr::Ip(a) => TcpStream::connect(a).await.unwrap(),
                    TargetAddr::Domain(h, p) => TcpStream::connect((h.as_str(), p)).await.unwrap(),
                };

                // Read Vision packet (16B uuid + 5B frame + content + padding)
                let mut hdr2 = [0u8; 5];
                s.read_exact(&mut hdr2).await.unwrap();
                let clen2 = u16::from_be_bytes([hdr2[3], hdr2[4]]) as usize;
                let mut ct2 = vec![0u8; clen2];
                s.read_exact(&mut ct2).await.unwrap();
                let (_, vpkt) = open_tls13(&c_app_key, &c_app_iv, &mut c_app_seq, &hdr2, &ct2);
                assert_eq!(&vpkt[..16], &uuid);
                let content_len = u16::from_be_bytes([vpkt[17], vpkt[18]]) as usize;
                let payload = &vpkt[21..21 + content_len];
                up.write_all(payload).await.unwrap();
                let mut echo = vec![0u8; content_len];
                up.read_exact(&mut echo).await.unwrap();

                // Reply VLESS resp header + Vision packet (16B uuid + cmd=0x02 Direct + echo)
                let mut resp = vec![0x00u8, 0x00u8];
                resp.extend_from_slice(&uuid);
                resp.push(0x02); // VISION_DIRECT
                resp.extend_from_slice(&(echo.len() as u16).to_be_bytes());
                resp.extend_from_slice(&0u16.to_be_bytes());
                resp.extend_from_slice(&echo);
                let enc = seal_tls13(&s_app_key, &s_app_iv, &mut s_app_seq, 0x17, &resp);
                s.write_all(&enc).await.unwrap();
            });
        }
    });
    addr
}

#[tokio::test]
async fn vless_tls_ws_round_trip() {
    let echo = spawn_echo_target().await;
    let uuid = VlessConfig::parse_uuid("11111111-2222-3333-4444-555555555555").unwrap();
    let vless_addr = spawn_vless_ws_server(uuid).await;
    let dialer = VlessDialer::new(VlessConfig {
        server: TargetAddr::Ip(vless_addr),
        uuid,
        flow: None,
        security: VlessSecurity::Tls(TlsConfig {
            sni: Some("localhost".into()),
            alpn: vec![],
            insecure: true,
        }),
        network: VlessNetwork::Ws(WsConfig {
            path: "/ws".into(),
            host: Some("localhost".into()),
        }),
    });
    let mut stream = dialer.dial_tcp(&TargetAddr::Ip(echo)).await.unwrap();
    let msg = b"hello vless tls ws!";
    stream.write_all(msg).await.unwrap();
    stream.flush().await.unwrap();
    let mut got = vec![0u8; msg.len()];
    stream.read_exact(&mut got).await.unwrap();
    assert_eq!(&got, msg);
}

#[tokio::test]
async fn vless_tls_grpc_round_trip() {
    let echo = spawn_echo_target().await;
    let uuid = VlessConfig::parse_uuid("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee").unwrap();
    let vless_addr = spawn_vless_grpc_server(uuid).await;
    let dialer = VlessDialer::new(VlessConfig {
        server: TargetAddr::Ip(vless_addr),
        uuid,
        flow: None,
        security: VlessSecurity::Tls(TlsConfig {
            sni: Some("localhost".into()),
            alpn: vec![b"h2".to_vec()],
            insecure: true,
        }),
        network: VlessNetwork::Grpc(GrpcConfig {
            service_name: "GunService".into(),
            authority: Some("localhost".into()),
        }),
    });
    let mut stream = dialer.dial_tcp(&TargetAddr::Ip(echo)).await.unwrap();
    let msg = b"hello vless tls grpc!";
    stream.write_all(msg).await.unwrap();
    stream.flush().await.unwrap();
    let mut got = vec![0u8; msg.len()];
    stream.read_exact(&mut got).await.unwrap();
    assert_eq!(&got, msg);
}

async fn reality_dialer(proof_case: ServerProof) -> VlessDialer {
    let uuid = VlessConfig::parse_uuid("99999999-8888-7777-6666-555555555555").unwrap();
    let srv_priv = StaticSecret::random_from_rng(rand::thread_rng());
    let srv_pub = PublicKey::from(&srv_priv);
    let pbk_b64 = URL_SAFE_NO_PAD.encode(srv_pub.as_bytes());
    let sid = [0xab, 0xcd, 0xef, 0x01];
    let srv_addr = spawn_reality_vless_vision_server(srv_priv, sid, proof_case, uuid).await;

    let reality_cfg = RealityConfig::from_strings("www.example.com", &pbk_b64, "abcdef01").unwrap();
    VlessDialer::new(VlessConfig {
        server: TargetAddr::Ip(srv_addr),
        uuid,
        flow: Some("xtls-rprx-vision".into()),
        security: VlessSecurity::Reality(reality_cfg),
        network: VlessNetwork::Tcp,
    })
}

#[tokio::test]
async fn reality_rejects_unauthenticated_server() {
    let target = TargetAddr::Ip("127.0.0.1:443".parse().unwrap());
    for (case, error) in [
        (ServerProof::BadCertificateMac, "invalid REALITY server certificate"),
        (ServerProof::BadTranscriptSignature, "invalid REALITY CertificateVerify"),
    ] {
        let dialer = reality_dialer(case).await;
        let err = dialer.dial_tcp(&target).await.err().expect("invalid proof accepted");
        assert_eq!(err.to_string(), error);
    }
}

#[tokio::test]
async fn vless_reality_vision_round_trip() {
    let echo = spawn_echo_target().await;
    let dialer = reality_dialer(ServerProof::Valid).await;
    let mut stream = dialer.dial_tcp(&TargetAddr::Ip(echo)).await.unwrap();
    let msg = b"hello vless reality vision!";
    stream.write_all(msg).await.unwrap();
    stream.flush().await.unwrap();
    let mut got = vec![0u8; msg.len()];
    stream.read_exact(&mut got).await.unwrap();
    assert_eq!(&got, msg);
}
