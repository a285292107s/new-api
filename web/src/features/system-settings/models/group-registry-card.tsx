/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { Pencil } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  getGroupRegistry,
  renameGroup,
  type GroupRegistryItem,
} from '@/lib/api'

function formatReferences(references?: Record<string, number>): string {
  if (!references) {
    return '-'
  }
  const parts = Object.entries(references)
    .filter(([, count]) => count > 0)
    .map(([name, count]) => `${name}: ${count}`)
  return parts.length > 0 ? parts.join(', ') : '-'
}

export function GroupRegistryCard() {
  const { t } = useTranslation()
  const [groups, setGroups] = useState<GroupRegistryItem[]>([])
  const [loading, setLoading] = useState(false)
  const [target, setTarget] = useState<GroupRegistryItem | null>(null)
  const [nextName, setNextName] = useState('')
  const [saving, setSaving] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const res = await getGroupRegistry()
      if (res.success && res.data) {
        setGroups(res.data)
      } else if (res.message) {
        toast.error(res.message)
      }
    } catch {
      toast.error(t('Failed to load the group registry'))
    } finally {
      setLoading(false)
    }
  }, [t])

  useEffect(() => {
    void load()
  }, [load])

  const openRename = (group: GroupRegistryItem) => {
    setTarget(group)
    setNextName(group.name)
  }

  const handleRename = async () => {
    if (!target) {
      return
    }
    const name = nextName.trim()
    if (!name) {
      toast.error(t('Group name is required'))
      return
    }
    setSaving(true)
    try {
      const res = await renameGroup(target.id, name)
      if (res.success) {
        toast.success(t('Group renamed to {{name}}', { name }))
        setTarget(null)
        await load()
      } else {
        toast.error(res.message ?? t('Failed to rename the group'))
      }
    } catch {
      toast.error(t('Failed to rename the group'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className='space-y-3'>
      <div className='flex items-center justify-between'>
        <div>
          <h3 className='text-base font-medium'>{t('Group Registry')}</h3>
          <p className='text-muted-foreground text-sm'>
            {t(
              'Rename a group here to update every channel, token, log and pricing entry in one transaction.'
            )}
          </p>
        </div>
        <Button
          variant='outline'
          size='sm'
          disabled={loading}
          onClick={() => void load()}
        >
          {t('Refresh')}
        </Button>
      </div>

      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('Name')}</TableHead>
            <TableHead>{t('Ratio')}</TableHead>
            <TableHead>{t('Topup Ratio')}</TableHead>
            <TableHead>{t('Auto')}</TableHead>
            <TableHead>{t('Description')}</TableHead>
            <TableHead>{t('References')}</TableHead>
            <TableHead className='text-right'>{t('Actions')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {groups.length === 0 ? (
            <TableRow>
              <TableCell colSpan={7} className='text-muted-foreground text-sm'>
                {loading ? t('Loading...') : t('No groups found')}
              </TableCell>
            </TableRow>
          ) : (
            groups.map((group) => (
              <TableRow key={group.id}>
                <TableCell className='font-medium'>{group.name}</TableCell>
                <TableCell>{group.ratio}</TableCell>
                <TableCell>{group.topup_ratio}</TableCell>
                <TableCell>{group.auto_enabled ? t('Yes') : '-'}</TableCell>
                <TableCell className='text-muted-foreground'>
                  {group.description || '-'}
                </TableCell>
                <TableCell className='text-muted-foreground text-xs'>
                  {formatReferences(group.references)}
                </TableCell>
                <TableCell className='text-right'>
                  <Button
                    variant='ghost'
                    size='sm'
                    disabled={group.name === 'default'}
                    onClick={() => openRename(group)}
                  >
                    <Pencil className='mr-1 size-3.5' />
                    {t('Rename')}
                  </Button>
                </TableCell>
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>

      <Dialog
        open={target !== null}
        onOpenChange={(open) => {
          if (!open) {
            setTarget(null)
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('Rename group')}</DialogTitle>
            <DialogDescription>
              {t(
                'Every reference to "{{name}}" will be updated atomically.',
                { name: target?.name ?? '' }
              )}
            </DialogDescription>
          </DialogHeader>
          <Input
            value={nextName}
            autoFocus
            onChange={(event) => setNextName(event.target.value)}
            placeholder={t('New group name')}
          />
          <DialogFooter>
            <Button variant='outline' onClick={() => setTarget(null)}>
              {t('Cancel')}
            </Button>
            <Button disabled={saving} onClick={() => void handleRename()}>
              {t('Confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
