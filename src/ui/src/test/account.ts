import type { Account } from '@/lib/account'

const ADDRESS = { street: '', line2: '', postal_code: '', city: '', country: '' }

/** The owner's Account as GET /api/v1/settings/account answers it; tests override what they look at. */
export const account = (o: Partial<Account> = {}): Account => ({
  owner_first_name: 'Alex', owner_last_name: 'Sample', owner_name_ru: '', phone: '', contacts: { phone: '', email: '' },
  address: ADDRESS, billing_same: true, billing: ADDRESS, card: null,
  ...o,
})
export const A = 'GET /api/v1/settings/account'
export const WA = 'GET /api/v1/whatsapp/status'
export const TG = 'GET /api/v1/telegram/status'
