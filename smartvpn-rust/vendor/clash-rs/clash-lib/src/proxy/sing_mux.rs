use super::{
    utils::RemoteConnector, AnyOutboundHandler, ConnectorType, DialWithConnector, OutboundHandler,
    OutboundType, PlainProxyAPIResponse,
};
use crate::{
    app::{
        dispatcher::{
            BoxedInstrumentedDatagram, BoxedInstrumentedStream, InstrumentedStream,
            InstrumentedStreamWrapper,
        },
        dns::ThreadSafeDNSResolver,
    },
    session::{Session, SocksAddr},
};
use async_trait::async_trait;
use smartvpn_sing_mux::{SessionPool, HOST, PORT};
use std::{io, sync::Arc};

use std::{
    pin::Pin,
    task::{Context, Poll},
};
use tokio::io::{AsyncRead, AsyncWrite, ReadBuf};
struct LogicalStream<S>(S);
impl<S: AsyncRead + Unpin> AsyncRead for LogicalStream<S> {
    fn poll_read(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        Pin::new(&mut self.0).poll_read(cx, buf)
    }
}
impl<S: AsyncWrite + Unpin> AsyncWrite for LogicalStream<S> {
    fn poll_write(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &[u8],
    ) -> Poll<io::Result<usize>> {
        Pin::new(&mut self.0).poll_write(cx, buf)
    }
    fn poll_flush(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.0).poll_flush(cx)
    }
    fn poll_shutdown(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Pin::new(&mut self.0).poll_shutdown(cx)
    }
}
impl<S: AsyncRead + AsyncWrite + Unpin + Send> super::ProxyStream for LogicalStream<S> {}

#[derive(Debug)]
struct Handler {
    inner: AnyOutboundHandler,
    pool: SessionPool,
}

pub(crate) fn wrap(inner: AnyOutboundHandler, enabled: bool) -> AnyOutboundHandler {
    if enabled {
        Arc::new(Handler {
            inner,
            pool: SessionPool::default(),
        })
    } else {
        inner
    }
}

impl Handler {
    async fn stream(
        &self,
        sess: &Session,
        resolver: ThreadSafeDNSResolver,
        via: Option<&dyn RemoteConnector>,
    ) -> io::Result<BoxedInstrumentedStream> {
        let mut header = vec![0, 0];
        sess.destination.write_buf(&mut header);
        let mut carrier = sess.clone();
        carrier.destination = SocksAddr::Domain(HOST.into(), PORT);
        let open = async {
            match via {
                Some(connector) => {
                    self.inner
                        .connect_stream_with_connector(&carrier, resolver, connector)
                        .await
                }
                None => self.inner.connect_stream(&carrier, resolver).await,
            }
        };
        let stream =
            InstrumentedStreamWrapper::new(LogicalStream(self.pool.open(header, open).await?));
        stream.append_to_chain(self.name()).await;
        Ok(Box::new(stream))
    }
}
#[async_trait]
impl DialWithConnector for Handler {
    fn support_dialer(&self) -> Option<&str> {
        self.inner.support_dialer()
    }
    async fn register_connector(&self, connector: Arc<dyn RemoteConnector>) {
        self.inner.register_connector(connector).await
    }
}
#[async_trait]
impl OutboundHandler for Handler {
    fn name(&self) -> &str {
        self.inner.name()
    }
    fn server_name(&self) -> Option<&str> {
        self.inner.server_name()
    }
    fn proto(&self) -> OutboundType {
        self.inner.proto()
    }
    async fn support_udp(&self) -> bool {
        self.inner.support_udp().await
    }
    async fn support_connector(&self) -> ConnectorType {
        self.inner.support_connector().await
    }
    fn try_as_plain_handler(&self) -> Option<&dyn PlainProxyAPIResponse> {
        self.inner.try_as_plain_handler()
    }
    async fn connect_stream(
        &self,
        sess: &Session,
        resolver: ThreadSafeDNSResolver,
    ) -> io::Result<BoxedInstrumentedStream> {
        self.stream(sess, resolver, None).await
    }
    async fn connect_stream_with_connector(
        &self,
        sess: &Session,
        resolver: ThreadSafeDNSResolver,
        connector: &dyn RemoteConnector,
    ) -> io::Result<BoxedInstrumentedStream> {
        self.stream(sess, resolver, Some(connector)).await
    }
    async fn connect_datagram(
        &self,
        sess: &Session,
        resolver: ThreadSafeDNSResolver,
    ) -> io::Result<BoxedInstrumentedDatagram> {
        self.inner.connect_datagram(sess, resolver).await
    }
    async fn connect_datagram_with_connector(
        &self,
        sess: &Session,
        resolver: ThreadSafeDNSResolver,
        connector: &dyn RemoteConnector,
    ) -> io::Result<BoxedInstrumentedDatagram> {
        self.inner
            .connect_datagram_with_connector(sess, resolver, connector)
            .await
    }
}
