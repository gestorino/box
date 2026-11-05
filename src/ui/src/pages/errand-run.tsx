import { useParams } from 'react-router'
import { RunDetail } from '@/components/run-detail'

export function ErrandRunPage() {
  const { run } = useParams()
  return <RunDetail id={run!} />
}
