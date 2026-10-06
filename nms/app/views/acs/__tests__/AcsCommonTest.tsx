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
import {act, renderHook} from '@testing-library/react-hooks';
import {usePoll} from '../AcsCommon';

type Deferred<T> = {promise: Promise<T>; resolve: (v: T) => void};

function deferred<T>(): Deferred<T> {
  let resolve: (v: T) => void = () => undefined;
  const promise = new Promise<T>(r => {
    resolve = r;
  });
  return {promise, resolve};
}

describe('usePoll', () => {
  it('drops the answer of a request made for the previous deps', async () => {
    const pending: Record<string, Deferred<string>> = {
      a: deferred<string>(),
      b: deferred<string>(),
    };
    const {result, rerender} = renderHook(
      ({key}: {key: string}) => usePoll(() => pending[key].promise, [key], 0),
      {initialProps: {key: 'a'}},
    );
    rerender({key: 'b'});
    await act(async () => {
      pending.b.resolve('b data');
      await pending.b.promise;
    });
    expect(result.current.data).toBe('b data');
    // The slow answer for "a" arrives last and must not win.
    await act(async () => {
      pending.a.resolve('a data');
      await pending.a.promise;
    });
    expect(result.current.data).toBe('b data');
  });

  it('clears the data when the deps change', async () => {
    const next = deferred<string>();
    let first = true;
    const {result, rerender} = renderHook(
      ({key}: {key: string}) =>
        usePoll(
          () => {
            if (first) {
              first = false;
              return Promise.resolve(`${key} data`);
            }
            return next.promise;
          },
          [key],
          0,
        ),
      {initialProps: {key: 'a'}},
    );
    await act(async () => {
      await Promise.resolve();
    });
    expect(result.current.data).toBe('a data');
    rerender({key: 'b'});
    expect(result.current.data).toBeNull();
    expect(result.current.isLoading).toBe(true);
  });
});
