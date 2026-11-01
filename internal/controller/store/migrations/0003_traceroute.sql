-- Traceroute support: a new probe kind and per-hop path data on results.
ALTER TABLE tests DROP CONSTRAINT tests_kind_check;
ALTER TABLE tests ADD CONSTRAINT tests_kind_check
    CHECK (kind IN ('icmp', 'dns', 'tcp', 'tls', 'http', 'traceroute'));

ALTER TABLE results ADD COLUMN hops jsonb;
