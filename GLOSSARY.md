# redProxy

redProxy forwards HTTP traffic to pools of backend servers according to a
single declarative configuration.

## Language

**Gateway**:
The assembled unit that serves traffic for one configuration. It owns the
routes, their backend pools, and the health of those backends, and is started
and stopped as one thing.
_Avoid_: app, runtime, server, proxy (for the whole system)

**Backend**:
A single server the gateway can forward a request to.
_Avoid_: upstream, node, target
