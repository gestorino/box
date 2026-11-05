import { useParams } from 'react-router'
import { RunDetail } from '@/components/run-detail'
import { accountPath, accountRunPath } from '@/lib/accounts'

/** A registration (or a sign-in) on its site's page: the run's own page, back to the site. */
export function AccountRunPage() {
  const { key = '', run = '' } = useParams()
  return <RunDetail id={run} noun="Run" back={{ to: accountPath(key), label: key }} logs={`${accountRunPath(key, Number(run))}/logs`} />
}
