/**
 * Copyright 2026 The Magma Authors.
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
import Alert from '@mui/material/Alert';
import Box from '@mui/material/Box';
import CardTitleRow from '../../components/layout/CardTitleRow';
import MenuItem from '@mui/material/MenuItem';
import Paper from '@mui/material/Paper';
import React, {useState} from 'react';
import ShowChartIcon from '@mui/icons-material/ShowChart';
import Skeleton from '@mui/material/Skeleton';
import TextField from '@mui/material/TextField';
import {
  CPE_KPIS,
  KpiPoint,
  cpeSelector,
  queryRange,
  toPoints,
} from './AcsMetrics';
import {EmptyBlock, RetryButton, usePoll} from './AcsCommon';
import {Line} from 'react-chartjs-2';

export const RANGES: Array<[string, number]> = [
  ['Last 3 hours', 3],
  ['Last 6 hours', 6],
  ['Last 12 hours', 12],
  ['Last 24 hours', 24],
  ['Last 7 days', 24 * 7],
];

export function RangeSelect({
  hours,
  onChange,
}: {
  hours: number;
  onChange: (hours: number) => void;
}) {
  return (
    <TextField
      select
      size="small"
      value={hours}
      onChange={e => onChange(parseInt(e.target.value, 10))}
      sx={{minWidth: 170}}
      inputProps={{'data-testid': 'acs-range'}}>
      {RANGES.map(([label, h]) => (
        <MenuItem key={h} value={h}>
          {label}
        </MenuItem>
      ))}
    </TextField>
  );
}

type Kpi = typeof CPE_KPIS[number];

function KpiChart({
  kpi,
  points,
  start,
  end,
}: {
  kpi: Kpi;
  points: Array<KpiPoint>;
  start: Date;
  end: Date;
}) {
  return (
    <Box sx={{height: 150}} data-testid={`chart-${kpi.metric}`}>
      <Line
        options={{
          maintainAspectRatio: false,
          animation: false,
          plugins: {
            legend: {display: false},
            title: {
              display: true,
              text: `${kpi.label} (${kpi.unit})`,
              align: 'start',
            },
          },
          scales: {
            x: {
              type: 'time',
              min: start.getTime(),
              max: end.getTime(),
              grid: {display: false},
            },
            y: {min: kpi.min, max: kpi.max},
          },
        }}
        data={{
          datasets: [
            {
              label: kpi.label,
              data: points,
              borderColor: kpi.color,
              backgroundColor: kpi.color,
              borderWidth: 1.8,
              pointRadius: 0,
              spanGaps: false,
            },
            {
              label: `${kpi.poor} poor`,
              data: [
                {x: start.getTime(), y: kpi.poor},
                {x: end.getTime(), y: kpi.poor},
              ],
              borderColor: '#E52240',
              borderDash: [4, 4],
              borderWidth: 1,
              pointRadius: 0,
            },
          ],
        }}
      />
    </Box>
  );
}

// RSRP, RSRQ and SINR history of one CPE from Prometheus (acsd exports the
// last reported value per cpe_key; metricsd scrapes it every minute).
export default function AcsKpiCharts({
  networkId,
  cpeKey,
}: {
  networkId: string;
  cpeKey: string;
}) {
  const [hours, setHours] = useState(24);
  const {data, error, isLoading, reload} = usePoll(
    async () => {
      const end = new Date();
      const start = new Date(end.getTime() - hours * 3600 * 1000);
      const series = await Promise.all(
        CPE_KPIS.map(k =>
          queryRange(networkId, cpeSelector(k.metric, cpeKey), start, end).then(
            toPoints,
          ),
        ),
      );
      return {start, end, series};
    },
    [networkId, cpeKey, hours],
    60000,
  );

  let body: React.ReactNode;
  if (isLoading && !data) {
    body = [0, 1, 2].map(i => (
      <Skeleton key={i} variant="rectangular" height={46} sx={{my: 1}} />
    ));
  } else if (error) {
    body = (
      <Alert severity="error" action={<RetryButton onClick={reload} />}>
        <b>KPI history is unavailable.</b> Prometheus did not answer the query
        for acs_rsrp_dbm. Current values in the Cellular card are still up to
        date.
      </Alert>
    );
  } else if (data && data.series.every(s => s.length === 0)) {
    body = (
      <EmptyBlock title="No KPI history yet">
        Recording starts at the CPE's next Inform; the first point shows a few
        minutes after that.
      </EmptyBlock>
    );
  } else if (data) {
    body = CPE_KPIS.map((k, i) => (
      <KpiChart
        key={k.metric}
        kpi={k}
        points={data.series[i]}
        start={data.start}
        end={data.end}
      />
    ));
  }

  return (
    <Paper elevation={0} sx={{p: 2, mb: 3}}>
      <CardTitleRow
        icon={ShowChartIcon}
        label="Radio KPIs · acs_rsrp_dbm, acs_rsrq_db, acs_sinr_db"
        filter={() => <RangeSelect hours={hours} onChange={setHours} />}
      />
      {body}
    </Paper>
  );
}
