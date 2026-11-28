use crate::dialer::{
    BoxFuture, BoxedStream, OutboundDialer, OutboundUdpDialer, TargetAddr, UdpPacketSocket,
};
use crate::protocol::vless::VlessNetwork;
use crate::transport::tls::{connect_tls, TlsConfig};
use bytes::{Buf, Bytes, BytesMut};
use futures_util::{SinkExt, StreamExt};
use marmo_proto::{vless::Uuid, vmess::*};
use rand::RngCore;
use std::{
    io,
    sync::Arc,
    time::{SystemTime, UNIX_EPOCH},
};
use tokio::io::{AsyncWriteExt, ReadHalf, WriteHalf};
use tokio::sync::{watch, Mutex};
use tokio_util::{
    codec::{Decoder, Encoder, FramedRead, FramedWrite},
    io::{CopyToBytes, SinkWriter, StreamReader},
};

#[derive(Clone)]
pub struct Config {
    pub server: TargetAddr,
    pub uuid: Uuid,
    pub security: Security,
    pub tls: Option<TlsConfig>,
    pub network: VlessNetwork,
}
#[derive(Clone)]
pub struct Dialer {
    cfg: Config,
    underlay: Arc<dyn OutboundDialer>,
}

struct ReadCodec {
    header: Option<Request>,
    chunks: ChunkDecoder,
}
impl Decoder for ReadCodec {
    type Item = Bytes;
    type Error = io::Error;
    fn decode(&mut self, src: &mut BytesMut) -> io::Result<Option<Bytes>> {
        if let Some(req) = &self.header {
            match response_progress(src, req) {
                ResponseProgress::NeedMore { .. } => return Ok(None),
                ResponseProgress::Invalid => {
                    return Err(io::Error::other("invalid VMess response authentication"))
                }
                ResponseProgress::Complete { consumed } => src.advance(consumed),
            }
            self.header = None;
        }
        match self.chunks.progress(src).map_err(io::Error::other)? {
            ChunkProgress::NeedMore { .. } => Ok(None),
            ChunkProgress::Complete { consumed, data } => {
                src.advance(consumed);
                Ok(Some(data.into()))
            }
        }
    }
}

struct WriteCodec(ChunkEncoder);
impl Encoder<Bytes> for WriteCodec {
    type Error = io::Error;
    fn encode(&mut self, data: Bytes, dst: &mut BytesMut) -> io::Result<()> {
        let mut frame = Vec::new();
        for chunk in data.chunks(CHUNK_PLAINTEXT_MAX) {
            self.0.encode(chunk, &mut frame).map_err(io::Error::other)?;
            dst.extend_from_slice(&frame);
        }
        Ok(())
    }
}
type Reader = FramedRead<ReadHalf<BoxedStream>, ReadCodec>;
type Writer = FramedWrite<WriteHalf<BoxedStream>, WriteCodec>;

impl Dialer {
    pub fn new(cfg: Config, underlay: Arc<dyn OutboundDialer>) -> Self {
        Self { cfg, underlay }
    }
    async fn open(&self, target: &TargetAddr, command: u8) -> io::Result<(Reader, Writer)> {
        let cfg = &self.cfg;
        let host = cfg.server.host();
        let mut stream = self.underlay.dial_tcp(&cfg.server).await?;
        if let Some(tls) = &cfg.tls {
            stream = connect_tls(stream, &host, tls).await?;
        }
        let mut stream = cfg.network.wrap(stream, &host).await?;
        let target = match target {
            TargetAddr::Ip(a) => marmo_net::Endpoint::from_ip(a.ip(), a.port()),
            TargetAddr::Domain(name, port) => {
                marmo_net::Endpoint::from_domain(name, *port).map_err(io::Error::other)?
            }
        };
        let key = CmdKey::derive(&cfg.uuid);
        let timestamp = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map_err(io::Error::other)?
            .as_secs();
        let secrets = RequestSecrets {
            body_iv: rand::random(),
            body_key: rand::random(),
            response_auth: rand::random(),
            auth_rand: rand::random(),
            connection_nonce: rand::random(),
        };
        let padding: [u8; 16] = rand::random();
        let spec = RequestSpec {
            target: &target,
            command,
            security: cfg.security,
            timestamp,
            padding: &padding[1..1 + usize::from(padding[0] & 15)],
            secrets: &secrets,
        };
        let mut header = [0; REQUEST_HEADER_MAX];
        let len = encode_request(&key, &spec, &mut header).map_err(io::Error::other)?;
        let RequestProgress::Complete(req) = request_progress(&header[..len], &key, timestamp)
        else {
            return Err(io::Error::other("VMess request codec mismatch"));
        };
        stream.write_all(&header[..len]).await?;
        stream.flush().await?;
        let (key, iv) = response_keys(&req.body_key, &req.body_iv);
        let chunks = ChunkDecoder::new(req.security, &key, &iv, req.option);
        let mut padding_source = [0; 64];
        rand::rngs::OsRng.fill_bytes(&mut padding_source);
        let encoder = ChunkEncoder::new(
            req.security,
            &req.body_key,
            &req.body_iv,
            req.option,
            padding_source,
        );
        let (read, write) = tokio::io::split(stream);
        Ok((
            FramedRead::new(
                read,
                ReadCodec {
                    header: Some(req),
                    chunks,
                },
            ),
            FramedWrite::new(write, WriteCodec(encoder)),
        ))
    }
}
impl OutboundDialer for Dialer {
    fn udp(&self) -> Option<&dyn OutboundUdpDialer> {
        Some(self)
    }
    fn dial_tcp<'a>(&'a self, target: &'a TargetAddr) -> BoxFuture<'a, io::Result<BoxedStream>> {
        Box::pin(async move {
            let (reader, writer) = self.open(target, COMMAND_TCP).await?;
            let chunks = reader.take_while(|chunk| {
                futures_util::future::ready(!matches!(chunk, Ok(data) if data.is_empty()))
            });
            Ok(Box::new(tokio::io::join(
                StreamReader::new(chunks),
                SinkWriter::new(CopyToBytes::new(writer)),
            )) as BoxedStream)
        })
    }
}
struct Association {
    dialer: Dialer,
    writer: Mutex<Option<(TargetAddr, Writer)>>,
    reader: watch::Sender<Option<Arc<Mutex<(TargetAddr, Reader)>>>>,
}
impl OutboundUdpDialer for Dialer {
    fn dial_udp(&self) -> BoxFuture<'_, io::Result<Box<dyn UdpPacketSocket>>> {
        Box::pin(async {
            Ok(Box::new(Association {
                dialer: self.clone(),
                writer: Mutex::new(None),
                reader: watch::channel(None).0,
            }) as Box<dyn UdpPacketSocket>)
        })
    }
}
impl UdpPacketSocket for Association {
    fn send_to<'a>(
        &'a self,
        payload: &'a [u8],
        target: &'a TargetAddr,
    ) -> BoxFuture<'a, io::Result<usize>> {
        Box::pin(async move {
            if payload.is_empty() || payload.len() > CHUNK_PLAINTEXT_MAX {
                return Err(io::Error::other(
                    "VMess UDP packet exceeds supported frame size or is empty",
                ));
            }
            let mut writer = self.writer.lock().await;
            if writer.is_none() {
                let (r, w) = self.dialer.open(target, COMMAND_UDP).await?;
                self.reader
                    .send_replace(Some(Arc::new(Mutex::new((target.clone(), r)))));
                *writer = Some((target.clone(), w));
            }
            let (peer, w) = writer.as_mut().unwrap();
            if peer != target {
                return Err(io::Error::other(
                    "VMess UDP association destination changed",
                ));
            }
            w.send(Bytes::copy_from_slice(payload)).await?;
            Ok(payload.len())
        })
    }
    fn recv_from<'a>(
        &'a self,
        buf: &'a mut [u8],
    ) -> BoxFuture<'a, io::Result<(usize, TargetAddr)>> {
        Box::pin(async move {
            let mut rx = self.reader.subscribe();
            let stream = rx
                .wait_for(Option::is_some)
                .await
                .map_err(io::Error::other)?
                .as_ref()
                .unwrap()
                .clone();
            let mut reader = stream.lock().await;
            let packet = reader
                .1
                .next()
                .await
                .ok_or_else(|| io::Error::other("VMess UDP stream closed"))??;
            if packet.len() > buf.len() {
                return Err(io::Error::other("VMess receive buffer too small"));
            }
            buf[..packet.len()].copy_from_slice(&packet);
            Ok((packet.len(), reader.0.clone()))
        })
    }
}
