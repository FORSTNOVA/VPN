//! Async TLS client transport powered by `tokio-rustls` and `ring`.

use std::{
    io,
    pin::Pin,
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc, Once,
    },
    task::{ready, Context, Poll},
};

use rustls::{
    client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier},
    pki_types::{CertificateDer, ServerName, UnixTime},
    ClientConfig, DigitallySignedStruct, RootCertStore, SignatureScheme,
};
use tokio::io::{AsyncRead, AsyncWrite, ReadBuf};
use tokio_rustls::{client::TlsStream, TlsConnector};

use crate::dialer::BoxedStream;

static CRYPTO_INIT: Once = Once::new();

pub fn ensure_crypto_provider() {
    CRYPTO_INIT.call_once(|| {
        let _ = rustls::crypto::ring::default_provider().install_default();
    });
}

/// TLS client parameters for proxy transports.
#[derive(Clone, Debug, Default)]
pub struct TlsConfig {
    pub sni: Option<String>,
    pub alpn: Vec<Vec<u8>>,
    pub insecure: bool,
}

#[derive(Debug)]
struct InsecureCertVerifier;

impl ServerCertVerifier for InsecureCertVerifier {
    fn verify_server_cert(
        &self,
        _end_entity: &CertificateDer<'_>,
        _intermediates: &[CertificateDer<'_>],
        _server_name: &ServerName<'_>,
        _ocsp_response: &[u8],
        _now: UnixTime,
    ) -> Result<ServerCertVerified, rustls::Error> {
        Ok(ServerCertVerified::assertion())
    }

    fn verify_tls12_signature(
        &self,
        _message: &[u8],
        _cert: &CertificateDer<'_>,
        _dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        Ok(HandshakeSignatureValid::assertion())
    }

    fn verify_tls13_signature(
        &self,
        _message: &[u8],
        _cert: &CertificateDer<'_>,
        _dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        Ok(HandshakeSignatureValid::assertion())
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        vec![
            SignatureScheme::RSA_PKCS1_SHA256,
            SignatureScheme::RSA_PKCS1_SHA384,
            SignatureScheme::RSA_PKCS1_SHA512,
            SignatureScheme::ECDSA_NISTP256_SHA256,
            SignatureScheme::ECDSA_NISTP384_SHA384,
            SignatureScheme::ECDSA_NISTP521_SHA512,
            SignatureScheme::RSA_PSS_SHA256,
            SignatureScheme::RSA_PSS_SHA384,
            SignatureScheme::RSA_PSS_SHA512,
            SignatureScheme::ED25519,
        ]
    }
}

pub(crate) fn client_config(cfg: &TlsConfig) -> ClientConfig {
    ensure_crypto_provider();
    let mut client_cfg = if cfg.insecure {
        ClientConfig::builder()
            .dangerous()
            .with_custom_certificate_verifier(Arc::new(InsecureCertVerifier))
            .with_no_client_auth()
    } else {
        let mut roots = RootCertStore::empty();
        roots.extend(webpki_roots::TLS_SERVER_ROOTS.iter().cloned());
        ClientConfig::builder()
            .with_root_certificates(roots)
            .with_no_client_auth()
    };
    if !cfg.alpn.is_empty() {
        client_cfg.alpn_protocols = cfg.alpn.clone();
    }

    client_cfg
}

/// Wraps an existing stream in a TLS client session.
pub async fn connect_tls(
    stream: BoxedStream,
    server_host: &str,
    cfg: &TlsConfig,
) -> io::Result<BoxedStream> {
    Ok(Box::new(handshake(stream, server_host, cfg).await?))
}

async fn handshake<S: AsyncRead + AsyncWrite + Unpin>(
    stream: S,
    server_host: &str,
    cfg: &TlsConfig,
) -> io::Result<TlsStream<S>> {
    let sni_host = cfg
        .sni
        .as_deref()
        .filter(|s| !s.is_empty())
        .unwrap_or(server_host)
        .to_string();
    let server_name = ServerName::try_from(sni_host.clone()).map_err(|_| {
        io::Error::new(
            io::ErrorKind::InvalidInput,
            format!("invalid TLS SNI/server name: {sni_host}"),
        )
    })?;

    let client_cfg = client_config(cfg);
    let connector = TlsConnector::from(Arc::new(client_cfg));
    connector
        .connect(server_name, stream)
        .await
        .map_err(|err| io::Error::new(io::ErrorKind::ConnectionAborted, err))
}

#[derive(Default)]
pub(crate) struct SpliceFlags {
    pub read: AtomicBool,
    pub write: AtomicBool,
}

/// Limit each rustls input read to the current TLS record. A raw inner record
/// can immediately follow the last encrypted Vision frame in the same TCP read.
struct RecordIo {
    raw: BoxedStream,
    header: [u8; 5],
    header_len: usize,
    payload_left: usize,
}

impl AsyncRead for RecordIo {
    fn poll_read(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        out: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        let this = self.as_mut().get_mut();
        let header = this.header_len < 5;
        let limit = if header {
            5 - this.header_len
        } else {
            this.payload_left
        };
        let mut part = ReadBuf::new(out.initialize_unfilled_to(limit.min(out.remaining())));
        ready!(Pin::new(&mut this.raw).poll_read(cx, &mut part))?;
        let bytes = part.filled();
        let n = bytes.len();
        if header {
            this.header[this.header_len..this.header_len + n].copy_from_slice(bytes);
            this.header_len += n;
            if this.header_len == 5 {
                this.payload_left = u16::from_be_bytes([this.header[3], this.header[4]]) as usize;
            }
        } else {
            this.payload_left -= n;
        }
        if this.header_len == 5 && this.payload_left == 0 {
            this.header_len = 0;
        }
        out.advance(n);
        Poll::Ready(Ok(()))
    }
}

impl AsyncWrite for RecordIo {
    fn poll_write(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &[u8],
    ) -> Poll<io::Result<usize>> {
        Pin::new(&mut self.raw).poll_write(cx, buf)
    }

    fn poll_flush(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.raw).poll_flush(cx)
    }

    fn poll_shutdown(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.raw).poll_shutdown(cx)
    }
}

struct SpliceTlsStream {
    tls: TlsStream<RecordIo>,
    flags: Arc<SpliceFlags>,
}

pub(crate) async fn connect_tls_spliceable(
    raw: BoxedStream,
    host: &str,
    cfg: &TlsConfig,
    flags: Arc<SpliceFlags>,
) -> io::Result<BoxedStream> {
    let io = RecordIo {
        raw,
        header: [0; 5],
        header_len: 0,
        payload_left: 0,
    };
    Ok(Box::new(SpliceTlsStream {
        tls: handshake(io, host, cfg).await?,
        flags,
    }))
}

impl AsyncRead for SpliceTlsStream {
    fn poll_read(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        out: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        if !self.flags.read.load(Ordering::Acquire) {
            return Pin::new(&mut self.tls).poll_read(cx, out);
        }
        let (io, conn) = self.tls.get_mut();
        match conn.reader().into_first_chunk() {
            Ok(bytes) if !bytes.is_empty() => {
                let n = bytes.len().min(out.remaining());
                out.put_slice(&bytes[..n]);
                std::io::BufRead::consume(&mut conn.reader(), n);
                return Poll::Ready(Ok(()));
            }
            Ok(_) => (),
            Err(e) if e.kind() == io::ErrorKind::WouldBlock => (),
            Err(e) => return Poll::Ready(Err(e)),
        }
        Pin::new(&mut io.raw).poll_read(cx, out)
    }
}

impl AsyncWrite for SpliceTlsStream {
    fn poll_write(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &[u8],
    ) -> Poll<io::Result<usize>> {
        if self.flags.write.load(Ordering::Acquire) {
            if self.tls.get_ref().1.wants_write() {
                ready!(Pin::new(&mut self.tls).poll_flush(cx))?;
            }
            return Pin::new(&mut self.tls.get_mut().0.raw).poll_write(cx, buf);
        }
        Pin::new(&mut self.tls).poll_write(cx, buf)
    }

    fn poll_flush(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.tls).poll_flush(cx)
    }

    fn poll_shutdown(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        if self.flags.write.load(Ordering::Acquire) {
            ready!(self.as_mut().poll_flush(cx))?;
            return Pin::new(&mut self.tls.get_mut().0.raw).poll_shutdown(cx);
        }
        Pin::new(&mut self.tls).poll_shutdown(cx)
    }
}
