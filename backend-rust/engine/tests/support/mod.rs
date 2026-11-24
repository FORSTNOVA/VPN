use std::{net::SocketAddr, sync::Arc};

use rand::RngCore;
use rustls::pki_types::{CertificateDer, PrivateKeyDer, PrivatePkcs8KeyDer};
use sha2::{Digest, Sha224, Sha256};
use smartvpn_engine::{
    dialer::TargetAddr,
    protocol::ss::{derive_subkey, evp_bytes_to_key, AeadCipher, ShadowsocksCipher},
    transport::tls::ensure_crypto_provider,
};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::{TcpListener, TcpStream},
};
use tokio_rustls::TlsAcceptor;

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

pub async fn spawn_echo_target() -> SocketAddr {
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

pub async fn spawn_ss_server(cipher_kind: ShadowsocksCipher, password: &str) -> SocketAddr {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let master_key = evp_bytes_to_key(password.as_bytes(), cipher_kind.key_len());
    tokio::spawn(async move {
        while let Ok((mut client, _)) = listener.accept().await {
            let master_key = master_key.clone();
            tokio::spawn(async move {
                let key_len = cipher_kind.key_len();
                let mut salt = vec![0u8; key_len];
                if client.read_exact(&mut salt).await.is_err() {
                    return;
                }
                let r_subkey = derive_subkey(&master_key, &salt, key_len).unwrap();

                let mut w_salt = vec![0u8; key_len];
                rand::thread_rng().fill_bytes(&mut w_salt);
                let w_subkey = derive_subkey(&master_key, &w_salt, key_len).unwrap();
                let mut enc = AeadCipher::new(cipher_kind, &w_subkey);

                // Read encrypted target addr + possible payload
                let mut len_buf = vec![0u8; 2 + 16];
                if client.read_exact(&mut len_buf).await.is_err() {
                    return;
                }
                if decrypt_aead(cipher_kind, &r_subkey, &mut 0u128, &mut len_buf).is_err() {
                    return;
                }
                let first_len = u16::from_be_bytes([len_buf[0], len_buf[1]]) as usize;
                let mut first = vec![0u8; first_len + 16];
                if client.read_exact(&mut first).await.is_err() {
                    return;
                }
                let mut r_nonce: u128 = 1;
                if decrypt_aead(cipher_kind, &r_subkey, &mut r_nonce, &mut first).is_err() {
                    return;
                }
                let mut cur = std::io::Cursor::new(first.as_slice());
                let target = TargetAddr::read_socks_addr(&mut cur).await.unwrap();
                let mut upstream = match target {
                    TargetAddr::Ip(a) => TcpStream::connect(a).await.unwrap(),
                    TargetAddr::Domain(h, p) => TcpStream::connect((h.as_str(), p)).await.unwrap(),
                };
                let leftover = &first[cur.position() as usize..];
                if !leftover.is_empty() {
                    upstream.write_all(leftover).await.unwrap();
                }
                client.write_all(&w_salt).await.unwrap();

                let (mut cr, mut cw) = client.into_split();
                let (mut ur, mut uw) = upstream.into_split();
                let r_key = r_subkey.clone();
                let c2u = tokio::spawn(async move {
                    let mut nonce = r_nonce;
                    loop {
                        let mut lb = vec![0u8; 2 + 16];
                        if cr.read_exact(&mut lb).await.is_err() {
                            break;
                        }
                        if decrypt_aead(cipher_kind, &r_key, &mut nonce, &mut lb).is_err() {
                            break;
                        }
                        let n = u16::from_be_bytes([lb[0], lb[1]]) as usize;
                        let mut pb = vec![0u8; n + 16];
                        if cr.read_exact(&mut pb).await.is_err() {
                            break;
                        }
                        if decrypt_aead(cipher_kind, &r_key, &mut nonce, &mut pb).is_err() {
                            break;
                        }
                        if uw.write_all(&pb).await.is_err() {
                            break;
                        }
                    }
                });
                let u2c = tokio::spawn(async move {
                    let mut buf = [0u8; 4096];
                    loop {
                        let Ok(n) = ur.read(&mut buf).await else {
                            break;
                        };
                        if n == 0 {
                            break;
                        }
                        let mut out = Vec::new();
                        if enc.seal_chunk(&buf[..n], &mut out).is_err() {
                            break;
                        }
                        if cw.write_all(&out).await.is_err() {
                            break;
                        }
                    }
                });
                let _ = tokio::join!(c2u, u2c);
            });
        }
    });
    addr
}

fn decrypt_aead(
    kind: ShadowsocksCipher,
    key: &[u8],
    nonce_counter: &mut u128,
    buf: &mut Vec<u8>,
) -> std::io::Result<()> {
    use aes_gcm::{
        aead::{AeadInPlace, KeyInit},
        Aes128Gcm, Aes256Gcm, Nonce,
    };
    use chacha20poly1305::ChaCha20Poly1305;
    let nonce_bytes = nonce_counter.to_le_bytes();
    let nonce = Nonce::from_slice(&nonce_bytes[..12]);
    let res = match kind {
        ShadowsocksCipher::Aes128Gcm => Aes128Gcm::new_from_slice(key)
            .unwrap()
            .decrypt_in_place(nonce, b"", buf),
        ShadowsocksCipher::Aes256Gcm => Aes256Gcm::new_from_slice(key)
            .unwrap()
            .decrypt_in_place(nonce, b"", buf),
        ShadowsocksCipher::ChaCha20Poly1305 => ChaCha20Poly1305::new_from_slice(key)
            .unwrap()
            .decrypt_in_place(nonce, b"", buf),
    };
    *nonce_counter += 1;
    res.map_err(|_| std::io::Error::new(std::io::ErrorKind::InvalidData, "decrypt failed"))
}

pub async fn spawn_trojan_server(password: &str) -> SocketAddr {
    spawn_trojan_server_with_target(password, |target| target).await
}
pub async fn spawn_trojan_server_with_target(
    password: &str,
    map: impl Fn(TargetAddr) -> TargetAddr + Copy + Send + Sync + 'static,
) -> SocketAddr {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let acceptor = test_tls_acceptor();
    let expected_hex: String = Sha224::digest(password.as_bytes())
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect();
    tokio::spawn(async move {
        while let Ok((tcp, _)) = listener.accept().await {
            let acceptor = acceptor.clone();
            let expected_hex = expected_hex.clone();
            tokio::spawn(async move {
                let mut tls = acceptor.accept(tcp).await.unwrap();
                let mut hash = [0u8; 58];
                tls.read_exact(&mut hash).await.unwrap();
                assert_eq!(&hash[..56], expected_hex.as_bytes());
                assert_eq!(&hash[56..58], b"\r\n");
                let mut cmd = [0u8; 1];
                tls.read_exact(&mut cmd).await.unwrap();
                assert_eq!(cmd[0], 0x01);
                let target = TargetAddr::read_socks_addr(&mut tls).await.unwrap();
                let mut crlf = [0u8; 2];
                tls.read_exact(&mut crlf).await.unwrap();
                assert_eq!(&crlf, b"\r\n");
                let mut upstream = match map(target) {
                    TargetAddr::Ip(a) => TcpStream::connect(a).await.unwrap(),
                    TargetAddr::Domain(h, p) => TcpStream::connect((h.as_str(), p)).await.unwrap(),
                };
                let _ = tokio::io::copy_bidirectional(&mut tls, &mut upstream).await;
            });
        }
    });
    addr
}

pub async fn spawn_anytls_server(password: &str) -> SocketAddr {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let acceptor = test_tls_acceptor();
    let expected_pw: [u8; 32] = Sha256::digest(password.as_bytes()).into();
    tokio::spawn(async move {
        while let Ok((tcp, _)) = listener.accept().await {
            let acceptor = acceptor.clone();
            tokio::spawn(async move {
                let mut tls = acceptor.accept(tcp).await.unwrap();
                let mut pw = [0u8; 32];
                tls.read_exact(&mut pw).await.unwrap();
                assert_eq!(pw, expected_pw);
                let mut pad_len = [0u8; 2];
                tls.read_exact(&mut pad_len).await.unwrap();
                let mut pad = vec![0u8; u16::from_be_bytes(pad_len) as usize];
                tls.read_exact(&mut pad).await.unwrap();

                let mut upstream: Option<TcpStream> = None;
                let mut sid_expected = 0u32;
                loop {
                    let mut hdr = [0u8; 7];
                    if tls.read_exact(&mut hdr).await.is_err() {
                        break;
                    }
                    let cmd = hdr[0];
                    let sid = u32::from_be_bytes([hdr[1], hdr[2], hdr[3], hdr[4]]);
                    let len = u16::from_be_bytes([hdr[5], hdr[6]]) as usize;
                    let mut payload = vec![0u8; len];
                    tls.read_exact(&mut payload).await.unwrap();

                    match cmd {
                        4 => {} // cmdSettings
                        1 => {
                            // cmdSYN -> reply cmdSYNACK + cmdWaste
                            sid_expected = sid;
                            let mut ack = vec![7u8];
                            ack.extend_from_slice(&sid.to_be_bytes());
                            ack.extend_from_slice(&0u16.to_be_bytes());
                            ack.push(0u8); // cmdWaste
                            ack.extend_from_slice(&0u32.to_be_bytes());
                            ack.extend_from_slice(&4u16.to_be_bytes());
                            ack.extend_from_slice(b"junk");
                            tls.write_all(&ack).await.unwrap();
                        }
                        2 if sid == sid_expected => {
                            if upstream.is_none() {
                                let mut cur = std::io::Cursor::new(payload.as_slice());
                                let target = TargetAddr::read_socks_addr(&mut cur).await.unwrap();
                                let mut up = match target {
                                    TargetAddr::Ip(a) => TcpStream::connect(a).await.unwrap(),
                                    TargetAddr::Domain(h, p) => {
                                        TcpStream::connect((h.as_str(), p)).await.unwrap()
                                    }
                                };
                                let rest = &payload[cur.position() as usize..];
                                if !rest.is_empty() {
                                    up.write_all(rest).await.unwrap();
                                    let mut echo = vec![0u8; rest.len()];
                                    up.read_exact(&mut echo).await.unwrap();
                                    let mut resp = vec![2u8];
                                    resp.extend_from_slice(&sid.to_be_bytes());
                                    resp.extend_from_slice(&(echo.len() as u16).to_be_bytes());
                                    resp.extend_from_slice(&echo);
                                    tls.write_all(&resp).await.unwrap();
                                }
                                upstream = Some(up);
                            } else if let Some(up) = upstream.as_mut() {
                                up.write_all(&payload).await.unwrap();
                                let mut echo = vec![0u8; payload.len()];
                                up.read_exact(&mut echo).await.unwrap();
                                let mut resp = vec![2u8];
                                resp.extend_from_slice(&sid.to_be_bytes());
                                resp.extend_from_slice(&(echo.len() as u16).to_be_bytes());
                                resp.extend_from_slice(&echo);
                                tls.write_all(&resp).await.unwrap();
                            }
                        }
                        3 => {
                            let mut fin = vec![3u8];
                            fin.extend_from_slice(&sid.to_be_bytes());
                            fin.extend_from_slice(&0u16.to_be_bytes());
                            let _ = tls.write_all(&fin).await;
                            break;
                        }
                        _ => {}
                    }
                }
            });
        }
    });
    addr
}
