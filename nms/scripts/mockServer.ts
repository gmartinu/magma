/**
 * Copyright 2020 The Magma Authors.
 *
 * This source code is licensed under the BSD-style license found in the
 * LICENSE file in the root directory of this source tree.
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
/* eslint-disable @typescript-eslint/no-unsafe-member-access */

import express from 'express';
import fs from 'fs';
import https from 'https';
import jsonServer from 'json-server';
import {
  FIXTURE_CPES,
  FIXTURE_PARAMETERS,
  FIXTURE_SIGNAL,
  FIXTURE_TASKS,
  fixtureDetail,
} from '../app/views/acs/AcsFixtures';

const certFile = process.env.API_CERT_FILENAME ?? '.cache/mock_server.cert';
const keyFile =
  process.env.API_PRIVATE_KEY_FILENAME ?? '.cache/mock_server.key';

const server = jsonServer.create();
const router = jsonServer.router('./mock/db.json');
const middlewares = jsonServer.defaults();
server.use(middlewares);

const buffer = fs.readFileSync('./mock/db.json', 'utf-8');
const db = JSON.parse(buffer) as Record<string, any>;

// Add feg and feg_lte handlers
server.post('/magma/v1/feg', (req, res) => {
  if (req.method === 'POST') {
    res.status(200).jsonp('Success');
  }
});

server.post('/magma/v1/feg_lte', (req, res) => {
  if (req.method === 'POST') {
    res.status(200).jsonp('Success');
  }
});
server.post('/magma/v1/networks/test_feg_network2/tiers', (req, res) => {
  if (req.method === 'POST') {
    res.status(200).jsonp('Success');
  }
});
server.post('/magma/v1/networks/test_feg_lte_network2/tiers', (req, res) => {
  if (req.method === 'POST') {
    res.status(200).jsonp('Success');
  }
});

server.put('/magma/v1/feg_lte/test_feg_lte_network', (req, res) => {
  if (req.method === 'PUT') {
    res.status(200).jsonp('Success');
  }
});

server.get('/magma/v1/feg_lte/test_feg_lte_network', (req, res) => {
  if (req.method === 'GET') {
    res.status(200).jsonp(db['networksFull']['test_feg_lte_network']);
  }
});

server.get('/magma/v1/lte/test', (req, res) => {
  if (req.method === 'GET') {
    res.status(200).jsonp(db['networksFull']['test']);
  }
});

server.put('/magma/v1/lte/test_network/cellular/epc', (req, res) => {
  if (req.method === 'PUT') {
    res.status(200).jsonp('Success');
  }
});

server.put('/magma/v1/lte/test_network/cellular/ran', (req, res) => {
  if (req.method === 'PUT') {
    res.status(200).jsonp('Success');
  }
});

// ACS (TR-069) sample data, so the ACS pages work without an Orchestrator.
// Registered before the per-network handlers below, which would otherwise
// answer the acs_* Prometheus queries with empty results.
const acsNetworks = ['test', 'test_feg_lte_network'];
acsNetworks.forEach(network => {
  const acs = `/magma/v1/acs/${network}`;
  server.get(`${acs}/cpes`, (req, res) => {
    const q = req.query as Record<string, string | undefined>;
    res
      .status(200)
      .jsonp(
        FIXTURE_CPES.filter(
          c =>
            (!q.mode || c.mode === q.mode) &&
            (!q.gateway_id || c.gateway_id === q.gateway_id) &&
            (q.online === undefined || String(c.online) === q.online),
        ),
      );
  });
  server.get(`${acs}/cpes/:cpeKey`, (req, res) => {
    const detail = fixtureDetail(req.params.cpeKey);
    if (detail) {
      res.status(200).jsonp(detail);
    } else {
      res.status(404).jsonp({message: 'CPE not found'});
    }
  });
  server.get(`${acs}/cpes/:cpeKey/parameters`, (req, res) => {
    const prefix = (req.query.prefix as string | undefined) ?? '';
    res
      .status(200)
      .jsonp(FIXTURE_PARAMETERS.filter(p => p.name.startsWith(prefix)));
  });
  server.get(`${acs}/cpes/:cpeKey/tasks`, (req, res) => {
    res
      .status(200)
      .jsonp(FIXTURE_TASKS.filter(t => t.cpe_key === req.params.cpeKey));
  });
  server.post(
    `${acs}/cpes/:cpeKey/tasks`,
    express.json(),
    (req: express.Request<{cpeKey: string}>, res: express.Response) => {
      const now = new Date().toISOString();
      res.status(201).jsonp({
        id: `t-${Date.now()}`,
        cpe_key: req.params.cpeKey,
        type: (req.body as {type?: string})?.type ?? 'refresh',
        status: 'pending',
        attempts: 0,
        max_attempts: 3,
        created: now,
        updated: now,
      });
    },
  );
  server.get(`/magma/v1/networks/${network}/entitlements`, (req, res) => {
    res
      .status(200)
      .jsonp({network_id: network, enforce: false, entitlements: []});
  });
  const promHandler = (
    req: {query: Record<string, unknown>},
    res: {status: (n: number) => {jsonp: (b: unknown) => void}},
    next: () => void,
  ) => {
    const query = String(req.query.query ?? '');
    const metric = ['acs_rsrp_dbm', 'acs_sinr_db', 'acs_rsrq_db'].find(m =>
      query.startsWith(m),
    );
    if (!metric) {
      return next();
    }
    const field = metric === 'acs_rsrp_dbm' ? 'rsrp' : 'sinr';
    const now = Math.floor(Date.now() / 1000);
    const result = Object.entries(FIXTURE_SIGNAL)
      .filter(([key]) => !query.includes('cpe_key') || query.includes(key))
      .map(([key, s]) => {
        const base = metric === 'acs_rsrq_db' ? -10 : s[field];
        return {
          metric: {cpe_key: key},
          value: [String(now), String(base)],
          values: Array.from({length: 288}, (_, i) => [
            String(now - (288 - i) * 300),
            String(base + Math.sin(i / 12) * 3),
          ]),
        };
      });
    res.status(200).jsonp({
      status: 'success',
      data: {resultType: 'matrix', result},
    });
  };
  server.get(`/magma/v1/networks/${network}/prometheus/query`, promHandler);
  server.get(
    `/magma/v1/networks/${network}/prometheus/query_range`,
    promHandler,
  );
});

const networks = ['test', 'test_feg_lte_network'];
networks.forEach(network => {
  server.get(`/magma/v1/networks/${network}/gateways`, (req, res) => {
    if (req.method === 'GET') {
      res.status(200).jsonp(db['networksFull'][network]['gateways']);
    }
  });

  server.get(`/magma/v1/networks/${network}/type`, (req, res) => {
    if (req.method === 'GET') {
      res.status(200).jsonp(db['networksFull'][network]['type']);
    }
  });

  server.get(`/magma/v1/lte/${network}/gateways`, (req, res) => {
    if (req.method === 'GET') {
      res.status(200).jsonp(db['lte']['gateways']);
    }
  });

  server.get(`/magma/v1/feg_lte/${network}/gateways`, (req, res) => {
    if (req.method === 'GET') {
      res.status(200).jsonp(db['feg_lte']['gateways']);
    }
  });

  server.get(
    `/magma/v1/networks/${network}/prometheus/query_range`,
    (req, res) => {
      if (req.method === 'GET') {
        res.status(200).jsonp({
          status: 'success',
          data: {
            resultType: 'vector',
            result: [],
          },
        });
      }
    },
  );

  server.get(`/magma/v1/networks/${network}/prometheus/query`, (req, res) => {
    if (req.method === 'GET') {
      res.status(200).jsonp({
        status: 'success',
        data: {
          resultType: 'vector',
          result: [],
        },
      });
    }
  });

  server.get(`/magma/v1/lte/${network}/subscribers`, (req, res) => {
    if (req.method === 'GET') {
      res.status(200).jsonp(db['subscribers']);
    }
  });

  server.post(`/magma/v1/lte/${network}/subscribers`, (req, res) => {
    if (req.method === 'POST') {
      res.status(200).jsonp('Success');
    }
  });

  // current set of subscribers
  const subscribers = [
    'IMSI001010002220018',
    'IMSI001010002220019',
    'IMSI001010002220020',
    'IMSI001010002220021',
    'IMSI001010002220022',
  ];
  subscribers.forEach(subscriberIMSI => {
    server.put(
      `/magma/v1/lte/${network}/subscribers/${subscriberIMSI}`,
      (req, res) => {
        if (req.method === 'PUT') {
          res.status(200).jsonp('Success');
        }
      },
    );

    server.get(
      `/magma/v1/lte/${network}/subscribers/${subscriberIMSI}`,
      (req, res) => {
        if (req.method === 'GET') {
          res.status(200).jsonp(db['subscribers'][`${subscriberIMSI}`]);
        }
      },
    );
  });

  // return empty base names
  server.get(
    `/magma/v1/lte/${network}/subscriber_config/base_names`,
    (req, res) => {
      if (req.method === 'GET') {
        res.status(200).jsonp([]);
      }
    },
  );

  // return empty rating groups
  server.get(`/magma/v1/networks/${network}/rating_groups`, (req, res) => {
    if (req.method === 'GET') {
      res.status(200).jsonp({});
    }
  });

  // return empty qos profiles
  server.get(`/magma/v1/lte/${network}/policy_qos_profiles`, (req, res) => {
    if (req.method === 'GET') {
      res.status(200).jsonp({});
    }
  });

  server.get(`/magma/v1/networks/${network}/policies/rules`, (req, res) => {
    if (req.method === 'GET') {
      res.status(200).jsonp(db['policies']);
    }
  });

  server.post(`/magma/v1/networks/${network}/policies/rules`, (req, res) => {
    if (req.method === 'POST') {
      res.status(200).jsonp('Success');
    }
  });

  // current set of policies
  const policies = ['test1', 'test2', 'test_policy0'];
  policies.forEach(policyName => {
    // TODO CLEANUP - e2e test specific mocks
    server.put(
      `/magma/v1/networks/${network}/policies/rules/${policyName}`,
      (req, res) => {
        if (req.method === 'PUT') {
          res.status(200).jsonp('Success');
        }
      },
    );

    server.get(
      `/magma/v1/networks/${network}/policies/rules/${policyName}`,
      (req, res) => {
        if (req.method === 'GET') {
          res.status(200).jsonp('Success');
        }
      },
    );
  });

  server.get(`/magma/v1/lte/${network}/apns`, (req, res) => {
    if (req.method === 'GET') {
      res.status(200).jsonp(db['apns']);
    }
  });

  server.post(`/magma/v1/lte/${network}/apns`, (req, res) => {
    if (req.method === 'POST') {
      res.status(200).jsonp('Success');
    }
  });

  // current set of apns
  const apns = ['internet', 'test_apn0'];
  apns.forEach(apnName => {
    server.put(`/magma/v1/lte/${network}/apns/${apnName}`, (req, res) => {
      if (req.method === 'PUT') {
        res.status(200).jsonp('Success');
      }
    });

    server.get(`/magma/v1/lte/${network}/apns/${apnName}`, (req, res) => {
      if (req.method === 'GET') {
        res.status(200).jsonp('Success');
      }
    });
  });

  server.get(`/magma/v1/events/${network}/about/count`, (req, res) => {
    if (req.method === 'GET') {
      res.status(200).jsonp(0);
    }
  });
  server.get(`/magma/v1/events/${network}`, (req, res) => {
    if (req.method === 'GET') {
      res.status(200).jsonp([]);
    }
  });
  server.get(`/magma/v1/lte/${network}/enodebs`, (req, res) => {
    if (req.method === 'GET') {
      res.status(200).jsonp(db['lte']['enodebs']);
    }
  });
  server.get(`/magma/v1/networks/${network}/tracing`, (req, res) => {
    if (req.method === 'GET') {
      res.status(200).jsonp([]);
    }
  });
  server.get(`/magma/v1/tenants`, (req, res) => {
    res.status(200).jsonp([]);
  });
  server.post(`/magma/v1/tenants`, (req, res) => {
    res.status(201).jsonp({});
  });
  server.put(`/magma/v1/tenants`, (req, res) => {
    res.status(204).jsonp({});
  });
  server.get(
    `/magma/v1/lte/{network}/enodebs/12020000051696P0013/state`,
    (req, res) => {
      if (req.method === 'GET') {
        res.status(200).jsonp(db['lte']['enodebState']['12020000051696P0013']);
      }
    },
  );
});

server.use('/magma/v1', router);

https
  .createServer(
    {
      key: fs.readFileSync(keyFile),
      cert: fs.readFileSync(certFile),
    },
    server,
  )
  .listen(3001, '0.0.0.0', () => {
    console.log('Go to https://localhost:3001/');
  });
