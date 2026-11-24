mod support;
use smartvpn_engine::{
    dialer::{OutboundDialer, TargetAddr},
    protocol::{
        anytls::{AnyTlsConfig, AnyTlsDialer},
        ss::{ShadowsocksCipher, ShadowsocksConfig, ShadowsocksDialer},
        trojan::{TrojanConfig, TrojanDialer},
    },
    transport::tls::TlsConfig,
};
use std::sync::Arc;
use support::*;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
#[tokio::test]
async fn shadowsocks_aead_round_trip() {
    let echo = spawn_echo_target().await;
    for cipher in [
        ShadowsocksCipher::Aes128Gcm,
        ShadowsocksCipher::Aes256Gcm,
        ShadowsocksCipher::ChaCha20Poly1305,
    ] {
        let ss_addr = spawn_ss_server(cipher, "ss-secret-pass").await;
        let dialer = ShadowsocksDialer::new(ShadowsocksConfig {
            server: TargetAddr::Ip(ss_addr),
            cipher,
            password: "ss-secret-pass".into(),
        });
        let mut stream = dialer.dial_tcp(&TargetAddr::Ip(echo)).await.unwrap();
        let msg = b"hello shadowsocks aead!";
        stream.write_all(msg).await.unwrap();
        stream.flush().await.unwrap();
        let mut got = vec![0u8; msg.len()];
        stream.read_exact(&mut got).await.unwrap();
        assert_eq!(&got, msg);
    }
}

#[tokio::test]
async fn trojan_tls_round_trip() {
    let echo = spawn_echo_target().await;
    let trojan_addr = spawn_trojan_server("trojan-pass").await;
    let dialer = TrojanDialer::new(TrojanConfig {
        server: TargetAddr::Ip(trojan_addr),
        password: "trojan-pass".into(),
        tls: TlsConfig {
            sni: Some("localhost".into()),
            alpn: vec![],
            insecure: true,
        },
    });
    let mut stream = dialer.dial_tcp(&TargetAddr::Ip(echo)).await.unwrap();
    let msg = b"hello trojan over tls!";
    stream.write_all(msg).await.unwrap();
    stream.flush().await.unwrap();
    let mut got = vec![0u8; msg.len()];
    stream.read_exact(&mut got).await.unwrap();
    assert_eq!(&got, msg);
}

#[tokio::test]
async fn anytls_round_trip_and_shutdown() {
    let echo = spawn_echo_target().await;
    let anytls_addr = spawn_anytls_server("anytls-pass").await;
    let dialer = AnyTlsDialer::new(AnyTlsConfig {
        server: TargetAddr::Ip(anytls_addr),
        password: "anytls-pass".into(),
        tls: TlsConfig {
            sni: Some("localhost".into()),
            alpn: vec![],
            insecure: true,
        },
    });
    let mut stream = dialer.dial_tcp(&TargetAddr::Ip(echo)).await.unwrap();
    let msg = b"hello anytls frame mux!";
    stream.write_all(msg).await.unwrap();
    stream.flush().await.unwrap();
    let mut got = vec![0u8; msg.len()];
    stream.read_exact(&mut got).await.unwrap();
    assert_eq!(&got, msg);
    stream.shutdown().await.unwrap();
    let mut eof = [0u8; 8];
    assert_eq!(stream.read(&mut eof).await.unwrap(), 0);
}

#[tokio::test]
async fn multi_hop_ss_trojan_anytls_chain() {
    let echo = spawn_echo_target().await;
    let ss_addr = spawn_ss_server(ShadowsocksCipher::Aes256Gcm, "hop1-ss").await;
    let trojan_addr = spawn_trojan_server("hop2-trojan").await;
    let anytls_addr = spawn_anytls_server("hop3-anytls").await;

    let hop1 = Arc::new(ShadowsocksDialer::new(ShadowsocksConfig {
        server: TargetAddr::Ip(ss_addr),
        cipher: ShadowsocksCipher::Aes256Gcm,
        password: "hop1-ss".into(),
    }));
    let hop2 = Arc::new(TrojanDialer::with_underlay(
        TrojanConfig {
            server: TargetAddr::Ip(trojan_addr),
            password: "hop2-trojan".into(),
            tls: TlsConfig {
                sni: Some("localhost".into()),
                alpn: vec![],
                insecure: true,
            },
        },
        hop1,
    ));
    let hop3 = AnyTlsDialer::with_underlay(
        AnyTlsConfig {
            server: TargetAddr::Ip(anytls_addr),
            password: "hop3-anytls".into(),
            tls: TlsConfig {
                sni: Some("localhost".into()),
                alpn: vec![],
                insecure: true,
            },
        },
        hop2,
    );

    let mut stream = hop3.dial_tcp(&TargetAddr::Ip(echo)).await.unwrap();
    let msg = b"3-hop pure rust chain works!";
    stream.write_all(msg).await.unwrap();
    stream.flush().await.unwrap();
    let mut got = vec![0u8; msg.len()];
    stream.read_exact(&mut got).await.unwrap();
    assert_eq!(&got, msg);
}
